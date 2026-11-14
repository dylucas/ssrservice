package store

import (
	"database/sql"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

const inboundColumns = `id, remark, listen, port, method, server_psk, udp_enabled, enabled, created_at, updated_at`

func scanInbound(row interface{ Scan(...any) error }) (*model.Inbound, error) {
	var (
		in      model.Inbound
		udp     int64
		enabled int64
	)
	if err := row.Scan(&in.ID, &in.Remark, &in.Listen, &in.Port, &in.Method, &in.ServerPSK,
		&udp, &enabled, &in.CreatedAt, &in.UpdatedAt); err != nil {
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
		res, err := tx.Exec(`INSERT INTO inbounds(remark, listen, port, method, server_psk, udp_enabled, enabled, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Remark, in.Listen, in.Port, in.Method, in.ServerPSK, udp, enabled, in.CreatedAt, in.UpdatedAt)
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
			udp_enabled = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			in.Remark, in.Listen, in.Port, in.Method, in.ServerPSK,
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
func (s *Store) DeleteInbound(id int64) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM inbounds WHERE id = ?`, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
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
