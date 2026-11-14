package store

import (
	"database/sql"
	"strings"
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

// tokensInUseChunk bounds the variable count of one existence query.
const tokensInUseChunk = 500

// TokensInUse returns the subset of tokens already assigned to a client.
//
// A bulk import has to allocate one token per created user. Checking them one
// at a time cost a query per row; this resolves the whole batch with a handful
// of queries instead.
func (s *Store) TokensInUse(tokens []string) (map[string]bool, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	taken := make(map[string]bool)
	for start := 0; start < len(tokens); start += tokensInUseChunk {
		end := start + tokensInUseChunk
		if end > len(tokens) {
			end = len(tokens)
		}
		chunk := tokens[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, token := range chunk {
			placeholders[i] = "?"
			args[i] = token
		}
		rows, err := s.db.Query(`SELECT sub_token FROM clients WHERE sub_token IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var token string
			if err := rows.Scan(&token); err != nil {
				rows.Close()
				return nil, err
			}
			taken[token] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return taken, nil
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

// ClientIdentity is the uniqueness projection an import needs: the name to
// reject duplicates and the enabled flag for the single-user-method rule.
// Reading only these columns keeps a bulk import from loading the PSKs of
// users it is not going to touch.
type ClientIdentity struct {
	ID      int64
	Name    string
	Enabled bool
}

// ListClientIdentities returns the identity of every client, optionally
// restricted to one inbound (0 = all), ordered by id.
func (s *Store) ListClientIdentities(inboundID int64) ([]ClientIdentity, error) {
	query := `SELECT id, name, enabled FROM clients`
	var (
		rows *sql.Rows
		err  error
	)
	if inboundID > 0 {
		rows, err = s.db.Query(query+` WHERE inbound_id = ? ORDER BY id`, inboundID)
	} else {
		rows, err = s.db.Query(query + ` ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make([]ClientIdentity, 0)
	for rows.Next() {
		var (
			identity ClientIdentity
			enabled  int64
		)
		if err := rows.Scan(&identity.ID, &identity.Name, &enabled); err != nil {
			return nil, err
		}
		identity.Enabled = enabled != 0
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

// createClientsChunk bounds how many rows go into one multi-row INSERT. Sixteen
// columns times 50 rows is 800 bound variables, well below SQLite's 32766
// limit; 50 was the fastest chunk measured for a 999 row import (chunks of 10,
// 20 and 50 all landed around 15ms, chunks of 100 and above got slower).
const createClientsChunk = 50

// CreateClients inserts a batch of clients in one transaction.
//
// The import path used to call CreateClient once per row: 999 rows cost 96ms,
// one transaction and one statement each, against 15ms for the chunked form.
// A bulk import is exactly the case where the store write lock is held for a
// long time, so the whole slice now goes through as few statements as possible.
// Ids are read back with RETURNING and land in the callers' slice in input
// order.
//
// A rejected chunk returns the error as-is (scanErr keeps ErrConflict), which
// lets the caller fall back to per-row inserts so every rejected line can still
// be reported with its own line number.
func (s *Store) CreateClients(clients []*model.Client) error {
	if len(clients) == 0 {
		return nil
	}
	now := time.Now().Unix()
	return s.write(func(tx *sql.Tx) error {
		for start := 0; start < len(clients); start += createClientsChunk {
			end := start + createClientsChunk
			if end > len(clients) {
				end = len(clients)
			}
			chunk := clients[start:end]
			var b strings.Builder
			b.WriteString(`INSERT INTO clients(inbound_id, name, psk, enabled, quota_bytes,
				up_bytes, down_bytes, expires_at, ip_limit, reset_policy, reset_day,
				last_reset_at, sub_token, note, created_at, updated_at) VALUES `)
			args := make([]any, 0, len(chunk)*16)
			for i, c := range chunk {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(`(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
				// Same defaults as CreateClient, so the batch and the single
				// insert stay interchangeable.
				if c.CreatedAt == 0 {
					c.CreatedAt = now
				}
				c.UpdatedAt = now
				if c.ResetPolicy == "" {
					c.ResetPolicy = model.ResetNone
				}
				args = append(args, c.InboundID, c.Name, c.PSK, boolInt(c.Enabled),
					c.QuotaBytes, c.UpBytes, c.DownBytes, c.ExpiresAt, c.IPLimit,
					c.ResetPolicy, c.ResetDay, c.LastResetAt, c.SubToken, c.Note,
					c.CreatedAt, c.UpdatedAt)
			}
			b.WriteString(` RETURNING id`)
			rows, err := tx.Query(b.String(), args...)
			if err != nil {
				return scanErr(err)
			}
			next := start
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				if next < len(clients) {
					clients[next].ID = id
					next++
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return scanErr(err)
			}
			rows.Close()
		}
		return nil
	})
}
