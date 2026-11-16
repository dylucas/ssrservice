package store

import (
	"fmt"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
)

// Inbound rollup reads used by the inbound detail page.
//
// traffic_daily is keyed by (client_id, day) and has no inbound dimension, so the
// per-inbound series joins the client rows. That is a primary-key-prefix scan over
// the clients of one inbound, not a table scan: the page is only ever asked for a
// handful of inbounds.

// InboundDailyTotals returns the per-day traffic of one inbound, oldest day
// first, for the days that have rows.
func (s *Store) InboundDailyTotals(inboundID int64, sinceDay string) ([]model.DailyUsage, error) {
	rows, err := s.db.Query(`SELECT t.day, SUM(t.up_bytes), SUM(t.down_bytes)
		FROM traffic_daily t JOIN clients c ON c.id = t.client_id
		WHERE c.inbound_id = ? AND t.day >= ?
		GROUP BY t.day ORDER BY t.day`, inboundID, sinceDay)
	if err != nil {
		return nil, fmt.Errorf("inbound daily totals: %w", err)
	}
	defer rows.Close()
	usage := make([]model.DailyUsage, 0, 32)
	for rows.Next() {
		var day model.DailyUsage
		if err := rows.Scan(&day.Day, &day.UpBytes, &day.DownBytes); err != nil {
			return nil, err
		}
		usage = append(usage, day)
	}
	return usage, rows.Err()
}

// InboundStatusCounts is the status distribution of one inbound's clients.
type InboundStatusCounts struct {
	Active        int64
	Disabled      int64
	Expired       int64
	QuotaExceeded int64
}

// InboundStatusBreakdown counts the clients of one inbound by computed status.
//
// The predicates reuse clientStatusClause, so the breakdown agrees with the
// filtered client list of the same inbound.
func (s *Store) InboundStatusBreakdown(inboundID, now int64) (InboundStatusCounts, error) {
	clauses := make([]string, 0, 4)
	// The predicates come first in the statement and the inbound id last, so the
	// arguments follow the same order.
	args := make([]any, 0, 8)
	for _, status := range []string{model.StatusActive, model.StatusDisabled,
		model.StatusExpired, model.StatusQuotaExceeded} {
		clause, clauseArgs, err := clientStatusClause(status, now)
		if err != nil {
			return InboundStatusCounts{}, err
		}
		// COALESCE covers an inbound with no clients: SUM over an empty set is
		// NULL, which does not scan into an int64.
		clauses = append(clauses, "COALESCE(SUM(CASE WHEN ("+clause+") THEN 1 ELSE 0 END), 0)")
		args = append(args, clauseArgs...)
	}
	args = append(args, inboundID)
	var counts InboundStatusCounts
	query := `SELECT ` + strings.Join(clauses, ", ") +
		` FROM clients c JOIN inbounds i ON i.id = c.inbound_id WHERE c.inbound_id = ?`
	if err := s.db.QueryRow(query, args...).Scan(&counts.Active, &counts.Disabled,
		&counts.Expired, &counts.QuotaExceeded); err != nil {
		return InboundStatusCounts{}, fmt.Errorf("inbound status breakdown: %w", err)
	}
	return counts, nil
}

// InboundTopClient is one row of the "busiest users" list of an inbound.
type InboundTopClient struct {
	ID        int64
	Name      string
	UpBytes   int64
	DownBytes int64
	Enabled   bool
}

// InboundTopClients returns the busiest clients of one inbound by total bytes.
func (s *Store) InboundTopClients(inboundID int64, limit int) ([]InboundTopClient, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.Query(`SELECT id, name, up_bytes, down_bytes, enabled
		FROM clients WHERE inbound_id = ?
		ORDER BY up_bytes + down_bytes DESC, id LIMIT ?`, inboundID, limit)
	if err != nil {
		return nil, fmt.Errorf("inbound top clients: %w", err)
	}
	defer rows.Close()
	top := make([]InboundTopClient, 0, limit)
	for rows.Next() {
		var (
			client  InboundTopClient
			enabled int64
		)
		if err := rows.Scan(&client.ID, &client.Name, &client.UpBytes, &client.DownBytes,
			&enabled); err != nil {
			return nil, err
		}
		client.Enabled = enabled != 0
		top = append(top, client)
	}
	return top, rows.Err()
}
