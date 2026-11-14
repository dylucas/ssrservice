package store

import (
	"database/sql"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

const inboundColumns = `id, remark, listen, port, method, server_psk, domain, domain_port, udp_enabled, enabled, created_at, updated_at`

func scanInbound(row interface{ Scan(...any) error }) (*model.Inbound, error) {
	var (
		in      model.Inbound
		udp     int64
		enabled int64
	)
	if err := row.Scan(&in.ID, &in.Remark, &in.Listen, &in.Port, &in.Method, &in.ServerPSK,
		&in.Domain, &in.DomainPort, &udp, &enabled, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	in.UDPEnabled = udp != 0
	in.Enabled = enabled != 0
	return &in, nil
}

// CreateInbound inserts an inbound and fills in its ID.
func (s *Store) CreateInbound(in *model.Inbound) error {
	now := time.Now().Unix()
	if in.CreatedAt == 0 {
		in.CreatedAt = now
	}
	in.UpdatedAt = now
	udp := boolInt(in.UDPEnabled)
	enabled := boolInt(in.Enabled)
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO inbounds(remark, listen, port, method, server_psk, domain, domain_port,
			udp_enabled, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Remark, in.Listen, in.Port, in.Method, in.ServerPSK, in.Domain, in.DomainPort,
			udp, enabled, in.CreatedAt, in.UpdatedAt)
		if err != nil {
			return scanErr(err)
		}
		in.ID, err = res.LastInsertId()
		return err
	})
	return err
}

// ListInbounds returns all inbounds ordered by id.
func (s *Store) ListInbounds() ([]model.Inbound, error) {
	rows, err := s.db.Query(`SELECT ` + inboundColumns + ` FROM inbounds ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inbounds := make([]model.Inbound, 0)
	for rows.Next() {
		in, err := scanInbound(rows)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, *in)
	}
	return inbounds, rows.Err()
}

// InboundByID returns one inbound.
func (s *Store) InboundByID(id int64) (*model.Inbound, error) {
	row := s.db.QueryRow(`SELECT `+inboundColumns+` FROM inbounds WHERE id = ?`, id)
	return scanInbound(row)
}

// UpdateInbound updates the mutable fields of an inbound.
func (s *Store) UpdateInbound(in *model.Inbound) error {
	in.UpdatedAt = time.Now().Unix()
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE inbounds SET remark = ?, listen = ?, port = ?, method = ?, server_psk = ?,
			domain = ?, domain_port = ?, udp_enabled = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			in.Remark, in.Listen, in.Port, in.Method, in.ServerPSK, in.Domain, in.DomainPort,
			boolInt(in.UDPEnabled), boolInt(in.Enabled), in.UpdatedAt, in.ID)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteInbound removes an inbound together with its clients and traffic rows.
// The per-day rollup is refreshed for every day the inbound's clients
// contributed to, because the cascade removes detail rows without touching it.
func (s *Store) DeleteInbound(id int64) error {
	return s.write(func(tx *sql.Tx) error {
		clientIDs, err := clientIDsOfInbound(tx, id)
		if err != nil {
			return err
		}
		days, err := dayKeysForClients(tx, clientIDs)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM inbounds WHERE id = ?`, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return refreshDailyTotals(tx, days)
	})
}

// clientIDsOfInbound returns the client ids of one inbound.
func clientIDsOfInbound(tx *sql.Tx, inboundID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT id FROM clients WHERE inbound_id = ?`, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PortInUse reports whether another inbound already uses listen:port.
func (s *Store) PortInUse(listen string, port int, excludeID int64) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM inbounds WHERE listen = ? AND port = ? AND id != ?`,
		listen, port, excludeID).Scan(&count)
	return count > 0, err
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// InboundAggregate holds the per-inbound counters the panel renders.
type InboundAggregate struct {
	InboundID          int64
	ClientCount        int
	EnabledClientCount int
	UpBytes            int64
	DownBytes          int64
}

// InboundAggregates returns the client counters of one inbound (inboundID > 0)
// or of every inbound, keyed by inbound id.
//
// The panel used to load every client row and sum them in Go to render a few
// numbers per inbound; SQLite can do that in a single grouped query that reads
// only five narrow columns. Inbounds without clients are simply absent from the
// result, so callers treat a missing key as a zero aggregate.
//
// The aggregate deliberately does not get a covering index: adding
// (inbound_id, enabled, up_bytes, down_bytes) made the 10s usage flush 41%
// slower (21.1ms -> 29.7ms for 5000 rows) to save 0.9ms here.
func (s *Store) InboundAggregates(inboundID int64) (map[int64]InboundAggregate, error) {
	query := `SELECT inbound_id, COUNT(*), COALESCE(SUM(enabled), 0),
		COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
		FROM clients`
	var (
		rows *sql.Rows
		err  error
	)
	if inboundID > 0 {
		rows, err = s.db.Query(query+` WHERE inbound_id = ? GROUP BY inbound_id`, inboundID)
	} else {
		rows, err = s.db.Query(query + ` GROUP BY inbound_id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aggregates := make(map[int64]InboundAggregate)
	for rows.Next() {
		var agg InboundAggregate
		if err := rows.Scan(&agg.InboundID, &agg.ClientCount, &agg.EnabledClientCount,
			&agg.UpBytes, &agg.DownBytes); err != nil {
			return nil, err
		}
		aggregates[agg.InboundID] = agg
	}
	return aggregates, rows.Err()
}
