package store

import (
	"database/sql"

	"github.com/ydong/ssrservice/internal/model"
)

// SaveUsage persists absolute traffic totals and daily increments for a batch
// of clients in one transaction. The two statements are prepared once per batch
// so a flush does not re-parse them for every client.
func (s *Store) SaveUsage(snaps []model.UsageSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	return s.write(func(tx *sql.Tx) error {
		updateTotals, err := tx.Prepare(`UPDATE clients SET up_bytes = ?, down_bytes = ? WHERE id = ?`)
		if err != nil {
			return err
		}
		defer updateTotals.Close()
		upsertDaily, err := tx.Prepare(`INSERT INTO traffic_daily(client_id, day, up_bytes, down_bytes)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(client_id, day) DO UPDATE SET
				up_bytes = up_bytes + excluded.up_bytes,
				down_bytes = down_bytes + excluded.down_bytes`)
		if err != nil {
			return err
		}
		defer upsertDaily.Close()
		for _, snap := range snaps {
			if _, err := updateTotals.Exec(snap.UpBytes, snap.DownBytes, snap.ClientID); err != nil {
				return err
			}
			if snap.DayUpBytes == 0 && snap.DayDownBytes == 0 {
				continue
			}
			if _, err := upsertDaily.Exec(snap.ClientID, snap.Day, snap.DayUpBytes, snap.DayDownBytes); err != nil {
				return err
			}
		}
		return nil
	})
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
func (s *Store) DailyTotals(sinceDay string) ([]model.DailyUsage, error) {
	rows, err := s.db.Query(`SELECT day, SUM(up_bytes), SUM(down_bytes) FROM traffic_daily
		WHERE day >= ? GROUP BY day ORDER BY day`, sinceDay)
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
