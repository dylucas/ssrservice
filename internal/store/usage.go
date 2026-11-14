package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
)

// usageWriteChunk bounds how many rows go into one multi-row statement. SQLite
// allows 32766 bound variables per statement and 500 terms in a compound SELECT
// by default; 200 rows (800 variables, 200 SELECT terms) stays well inside both.
const usageWriteChunk = 200

// SaveUsage persists absolute traffic totals and daily increments for a batch of
// clients in one transaction.
//
// The rows are written as chunked multi-row statements instead of one statement
// per client: a flush on a 5000 client deployment used to issue 10,000
// statements (measured 122ms, all of it while holding the store write lock). The
// per-day rollup the dashboard reads is maintained in the same transaction, so
// it can never drift from the detail rows.
func (s *Store) SaveUsage(snaps []model.UsageSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	return s.write(func(tx *sql.Tx) error {
		if err := upsertClientTotals(tx, snaps); err != nil {
			return err
		}
		if err := upsertDailyUsage(tx, snaps); err != nil {
			return err
		}
		return addDailyTotals(tx, snaps)
	})
}

// upsertClientTotals writes the absolute counters with a chunked UPDATE ... FROM.
func upsertClientTotals(tx *sql.Tx, snaps []model.UsageSnapshot) error {
	for start := 0; start < len(snaps); start += usageWriteChunk {
		end := start + usageWriteChunk
		if end > len(snaps) {
			end = len(snaps)
		}
		chunk := snaps[start:end]
		var b strings.Builder
		b.WriteString(`UPDATE clients SET up_bytes = v.up, down_bytes = v.down FROM (`)
		args := make([]any, 0, len(chunk)*3)
		for i, snap := range chunk {
			if i > 0 {
				b.WriteString(` UNION ALL `)
			}
			b.WriteString(`SELECT ? AS id, ? AS up, ? AS down`)
			args = append(args, snap.ClientID, snap.UpBytes, snap.DownBytes)
		}
		b.WriteString(`) AS v WHERE clients.id = v.id`)
		if _, err := tx.Exec(b.String(), args...); err != nil {
			return fmt.Errorf("save client totals: %w", err)
		}
	}
	return nil
}

// upsertDailyUsage accumulates the per-day increments, skipping zero deltas.
func upsertDailyUsage(tx *sql.Tx, snaps []model.UsageSnapshot) error {
	pending := make([]model.UsageSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.DayUpBytes == 0 && snap.DayDownBytes == 0 {
			continue
		}
		pending = append(pending, snap)
	}
	for start := 0; start < len(pending); start += usageWriteChunk {
		end := start + usageWriteChunk
		if end > len(pending) {
			end = len(pending)
		}
		chunk := pending[start:end]
		var b strings.Builder
		b.WriteString(`INSERT INTO traffic_daily(client_id, day, up_bytes, down_bytes) VALUES `)
		args := make([]any, 0, len(chunk)*4)
		for i, snap := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?,?,?)`)
			args = append(args, snap.ClientID, snap.Day, snap.DayUpBytes, snap.DayDownBytes)
		}
		b.WriteString(` ON CONFLICT(client_id, day) DO UPDATE SET
			up_bytes = up_bytes + excluded.up_bytes,
			down_bytes = down_bytes + excluded.down_bytes`)
		if _, err := tx.Exec(b.String(), args...); err != nil {
			return fmt.Errorf("save daily usage: %w", err)
		}
	}
	return nil
}

// addDailyTotals folds the batch's per-day increments into the rollup table.
// A batch usually touches one day, so this is normally a single row write.
func addDailyTotals(tx *sql.Tx, snaps []model.UsageSnapshot) error {
	type delta struct{ up, down int64 }
	deltas := make(map[string]*delta)
	for _, snap := range snaps {
		if snap.DayUpBytes == 0 && snap.DayDownBytes == 0 {
			continue
		}
		d := deltas[snap.Day]
		if d == nil {
			d = &delta{}
			deltas[snap.Day] = d
		}
		d.up += snap.DayUpBytes
		d.down += snap.DayDownBytes
	}
	for day, d := range deltas {
		if _, err := tx.Exec(`INSERT INTO traffic_daily_totals(day, up_bytes, down_bytes) VALUES(?, ?, ?)
			ON CONFLICT(day) DO UPDATE SET
				up_bytes = up_bytes + excluded.up_bytes,
				down_bytes = down_bytes + excluded.down_bytes`, day, d.up, d.down); err != nil {
			return fmt.Errorf("save daily totals: %w", err)
		}
	}
	return nil
}

// DailyUsage returns the daily history of a client since the given day
// (inclusive), ordered by day ascending.
func (s *Store) DailyUsage(clientID int64, sinceDay string) ([]model.DailyUsage, error) {
	rows, err := s.db.Query(`SELECT day, up_bytes, down_bytes FROM traffic_daily
		WHERE client_id = ? AND day >= ? ORDER BY day`, clientID, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := make([]model.DailyUsage, 0)
	for rows.Next() {
		var day model.DailyUsage
		if err := rows.Scan(&day.Day, &day.UpBytes, &day.DownBytes); err != nil {
			return nil, err
		}
		usage = append(usage, day)
	}
	return usage, rows.Err()
}

// DailyTotals returns traffic grouped by day across all clients.
//
// It reads the per-day rollup instead of aggregating traffic_daily: on a 5000
// client deployment the detail table holds ~155k rows in a 30 day window, which
// took 30ms per dashboard poll to aggregate for 31 numbers. The rollup is
// maintained by SaveUsage and the delete paths in the same transactions as the
// detail rows.
func (s *Store) DailyTotals(sinceDay string) ([]model.DailyUsage, error) {
	rows, err := s.db.Query(`SELECT day, up_bytes, down_bytes FROM traffic_daily_totals
		WHERE day >= ? ORDER BY day`, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := make([]model.DailyUsage, 0)
	for rows.Next() {
		var day model.DailyUsage
		if err := rows.Scan(&day.Day, &day.UpBytes, &day.DownBytes); err != nil {
			return nil, err
		}
		usage = append(usage, day)
	}
	return usage, rows.Err()
}

// dayKeysForClients returns the distinct days that have detail rows for the given
// clients. It is used before a delete so the affected rollup days can be
// refreshed afterwards (a cascade removes detail rows without touching the
// rollup).
func dayKeysForClients(tx *sql.Tx, clientIDs []int64) ([]string, error) {
	if len(clientIDs) == 0 {
		return nil, nil
	}
	days := make(map[string]struct{})
	for start := 0; start < len(clientIDs); start += usageWriteChunk {
		end := start + usageWriteChunk
		if end > len(clientIDs) {
			end = len(clientIDs)
		}
		chunk := clientIDs[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := tx.Query(`SELECT DISTINCT day FROM traffic_daily WHERE client_id IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var day string
			if err := rows.Scan(&day); err != nil {
				rows.Close()
				return nil, err
			}
			days[day] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	keys := make([]string, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	return keys, nil
}

// refreshDailyTotals recomputes the rollup rows for the given days from the
// detail table. Days whose detail rows are all gone are removed from the rollup.
func refreshDailyTotals(tx *sql.Tx, days []string) error {
	for start := 0; start < len(days); start += usageWriteChunk {
		end := start + usageWriteChunk
		if end > len(days) {
			end = len(days)
		}
		chunk := days[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, day := range chunk {
			placeholders[i] = "?"
			args[i] = day
		}
		list := strings.Join(placeholders, ",")
		if _, err := tx.Exec(`DELETE FROM traffic_daily_totals WHERE day IN (`+list+`)`, args...); err != nil {
			return fmt.Errorf("clear daily totals: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO traffic_daily_totals(day, up_bytes, down_bytes)
			SELECT day, COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
			FROM traffic_daily WHERE day IN (`+list+`) GROUP BY day`, args...); err != nil {
			return fmt.Errorf("refresh daily totals: %w", err)
		}
	}
	return nil
}

// RebuildDailyTotals recomputes the whole rollup from the detail table. It is a
// repair tool: normal writes keep the rollup in sync transactionally.
func (s *Store) RebuildDailyTotals() error {
	return s.write(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM traffic_daily_totals`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO traffic_daily_totals(day, up_bytes, down_bytes)
			SELECT day, COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
			FROM traffic_daily GROUP BY day`)
		return err
	})
}

// TrafficTotals returns the sum of all client counters.
func (s *Store) TrafficTotals() (up, down int64, err error) {
	err = s.db.QueryRow(`SELECT COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0) FROM clients`).Scan(&up, &down)
	return up, down, err
}

// ClientsForReset returns clients whose last_reset_at is older than the given
// reset instant and whose policy is monthly.
func (s *Store) ClientsForReset(now int64) ([]model.Client, error) {
	rows, err := s.db.Query(`SELECT `+clientColumns+` FROM clients
		WHERE reset_policy = 'monthly' AND last_reset_at < ?`, now)
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
