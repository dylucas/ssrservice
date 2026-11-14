package store

import (
	"database/sql"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

const clientColumns = `id, inbound_id, name, psk, enabled, quota_bytes, up_bytes, down_bytes,
	expires_at, ip_limit, reset_policy, reset_day, last_reset_at, sub_token, note, created_at, updated_at`

func scanClient(row interface{ Scan(...any) error }) (*model.Client, error) {
	var (
		c       model.Client
		enabled int64
	)
	if err := row.Scan(&c.ID, &c.InboundID, &c.Name, &c.PSK, &enabled, &c.QuotaBytes,
		&c.UpBytes, &c.DownBytes, &c.ExpiresAt, &c.IPLimit, &c.ResetPolicy,
		&c.ResetDay, &c.LastResetAt, &c.SubToken, &c.Note, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, scanErr(err)
	}
	c.Enabled = enabled != 0
	return &c, nil
}

// CreateClient inserts a client and fills in its ID.
func (s *Store) CreateClient(c *model.Client) error {
	now := time.Now().Unix()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.ResetPolicy == "" {
		c.ResetPolicy = model.ResetNone
	}
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO clients(inbound_id, name, psk, enabled, quota_bytes, up_bytes, down_bytes,
			expires_at, ip_limit, reset_policy, reset_day, last_reset_at, sub_token, note, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.InboundID, c.Name, c.PSK, boolInt(c.Enabled), c.QuotaBytes, c.UpBytes, c.DownBytes,
			c.ExpiresAt, c.IPLimit, c.ResetPolicy, c.ResetDay, c.LastResetAt, c.SubToken, c.Note,
			c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return scanErr(err)
		}
		c.ID, err = res.LastInsertId()
		return err
	})
	return err
}

// ListClients returns clients, optionally filtered by inbound id (0 = all).
func (s *Store) ListClients(inboundID int64) ([]model.Client, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if inboundID > 0 {
		rows, err = s.db.Query(`SELECT `+clientColumns+` FROM clients WHERE inbound_id = ? ORDER BY id`, inboundID)
	} else {
		rows, err = s.db.Query(`SELECT ` + clientColumns + ` FROM clients ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := make([]model.Client, 0)
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		clients = append(clients, *c)
	}
	return clients, rows.Err()
}

// ClientByID returns one client.
func (s *Store) ClientByID(id int64) (*model.Client, error) {
	row := s.db.QueryRow(`SELECT `+clientColumns+` FROM clients WHERE id = ?`, id)
	return scanClient(row)
}

// ClientBySubToken returns the client owning a subscription token.
func (s *Store) ClientBySubToken(token string) (*model.Client, error) {
	row := s.db.QueryRow(`SELECT `+clientColumns+` FROM clients WHERE sub_token = ?`, token)
	return scanClient(row)
}

// UpdateClient updates user-editable fields. Traffic counters are excluded;
// they are written by SaveUsage.
func (s *Store) UpdateClient(c *model.Client) error {
	c.UpdatedAt = time.Now().Unix()
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE clients SET name = ?, psk = ?, enabled = ?, quota_bytes = ?,
			expires_at = ?, ip_limit = ?, reset_policy = ?, reset_day = ?, note = ?, updated_at = ?
			WHERE id = ?`,
			c.Name, c.PSK, boolInt(c.Enabled), c.QuotaBytes, c.ExpiresAt, c.IPLimit,
			c.ResetPolicy, c.ResetDay, c.Note, c.UpdatedAt, c.ID)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// DeleteClient removes a client and its traffic history. The per-day rollup is
// refreshed for the days the client contributed to, since the cascade removes
// detail rows without touching it.
func (s *Store) DeleteClient(id int64) error {
	return s.write(func(tx *sql.Tx) error {
		days, err := dayKeysForClients(tx, []int64{id})
		if err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM clients WHERE id = ?`, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return refreshDailyTotals(tx, days)
	})
}

// SetClientEnabled toggles a client.
func (s *Store) SetClientEnabled(id int64, enabled bool) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE clients SET enabled = ?, updated_at = ? WHERE id = ?`,
			boolInt(enabled), time.Now().Unix(), id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResetClientTraffic zeroes a client's counters.
func (s *Store) ResetClientTraffic(id int64, resetAt int64) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE clients SET up_bytes = 0, down_bytes = 0, last_reset_at = ?, updated_at = ?
			WHERE id = ?`, resetAt, time.Now().Unix(), id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// UpdateClientToken replaces the subscription token.
func (s *Store) UpdateClientToken(id int64, token string) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE clients SET sub_token = ?, updated_at = ? WHERE id = ?`,
			token, time.Now().Unix(), id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// clientRowColumns is the narrow projection the client list renders. It skips
// the credential and audit columns (psk, sub_token, last_reset_at, created_at,
// updated_at) that the list API no longer returns, so a list request does not
// pay to read and decode them.
const clientRowColumns = `id, inbound_id, name, enabled, quota_bytes, up_bytes, down_bytes,
	expires_at, ip_limit, reset_policy, reset_day, note`

// ClientRow is the lean list projection of a client.
type ClientRow struct {
	ID          int64
	InboundID   int64
	Name        string
	Enabled     bool
	QuotaBytes  int64
	UpBytes     int64
	DownBytes   int64
	ExpiresAt   int64
	IPLimit     int
	ResetPolicy string
	ResetDay    int
	Note        string
}

// Model projects the row back onto the domain type. Only the fields the
// projection carries are set; callers must not rely on psk, sub_token or the
// audit timestamps being populated.
func (c ClientRow) Model() model.Client {
	return model.Client{
		ID:          c.ID,
		InboundID:   c.InboundID,
		Name:        c.Name,
		Enabled:     c.Enabled,
		QuotaBytes:  c.QuotaBytes,
		UpBytes:     c.UpBytes,
		DownBytes:   c.DownBytes,
		ExpiresAt:   c.ExpiresAt,
		IPLimit:     c.IPLimit,
		ResetPolicy: c.ResetPolicy,
		ResetDay:    c.ResetDay,
		Note:        c.Note,
	}
}

// ListClientRows returns the list projection, optionally filtered by inbound id
// (0 = all), ordered by id.
func (s *Store) ListClientRows(inboundID int64) ([]ClientRow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if inboundID > 0 {
		rows, err = s.db.Query(`SELECT `+clientRowColumns+` FROM clients WHERE inbound_id = ? ORDER BY id`, inboundID)
	} else {
		rows, err = s.db.Query(`SELECT ` + clientRowColumns + ` FROM clients ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := make([]ClientRow, 0)
	for rows.Next() {
		var (
			c       ClientRow
			enabled int64
		)
		if err := rows.Scan(&c.ID, &c.InboundID, &c.Name, &enabled, &c.QuotaBytes,
			&c.UpBytes, &c.DownBytes, &c.ExpiresAt, &c.IPLimit, &c.ResetPolicy,
			&c.ResetDay, &c.Note); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		clients = append(clients, c)
	}
	return clients, rows.Err()
}
