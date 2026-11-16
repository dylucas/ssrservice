package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// Bounds for the access analytics endpoints. They keep a single request from
// asking for an unbounded result set: raw events are short lived but a busy
// instance can still have tens of thousands of rows in the 7 day window.
const (
	AccessEventDefaultLimit = 100
	AccessEventMaxLimit     = 500
	AccessEventMaxOffset    = 100000
	AccessQueryMaxLen       = 128

	accessTopDefault = 10
	accessTopMax     = 50

	// accessCountCap bounds how many matching raw events the listing counts.
	// The offset cap already makes anything beyond it unreachable, so counting
	// past it only walks rows nobody can page to.
	accessCountCap = AccessEventMaxOffset + 1
	// accessPruneBatch bounds one DELETE. Retention is a background chore, so
	// it must release the store write lock between batches instead of holding
	// it for however long a large backlog takes.
	accessPruneBatch = 5000
	// accessPruneBudget bounds one retention run. A tick that runs out of budget
	// leaves the rest for the next one; the per-run allowance is far above what
	// a single instance accumulates in the interval between ticks, so a backlog
	// still drains.
	accessPruneBudget = 500 * time.Millisecond
	// accessFoldMaxKeys bounds how many rollup keys one cardinality fold
	// touches, for the same reason: the fold must not turn into a long burst of
	// writes on the connection that traffic accounting shares.
	accessFoldMaxKeys = 200
)

// accessWriteChunk mirrors usageWriteChunk: 200 rows of 12 (events) or 10
// (rollup) columns stay well inside SQLite's bound-variable limit.
const accessWriteChunk = 200

// AccessEventFilter selects raw access records. Zero values mean "no
// constraint" except Limit/Offset, which take the documented defaults.
type AccessEventFilter struct {
	ClientID  int64
	InboundID int64
	Outcome   string
	Network   string
	// Query is a case-insensitive substring matched against the destination
	// host.
	Query  string
	Since  int64 // inclusive, unix seconds
	Until  int64 // exclusive, unix seconds
	Limit  int
	Offset int
}

// NormalizeAccessPage applies the documented defaults and bounds without
// hiding a bad value: handlers validate their arguments first, and this exists
// for callers that construct a filter directly.
func NormalizeAccessPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = AccessEventDefaultLimit
	}
	if limit > AccessEventMaxLimit {
		limit = AccessEventMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (f AccessEventFilter) normalized() AccessEventFilter {
	f.Outcome = strings.ToLower(strings.TrimSpace(f.Outcome))
	if f.Outcome == "all" {
		f.Outcome = ""
	}
	f.Network = strings.ToLower(strings.TrimSpace(f.Network))
	if f.Network == "all" {
		f.Network = ""
	}
	f.Query = strings.TrimSpace(f.Query)
	f.Limit, f.Offset = NormalizeAccessPage(f.Limit, f.Offset)
	return f
}

// where renders the filter predicate and its arguments. The table is aliased
// as e because the listing joins the client and inbound names.
func (f AccessEventFilter) where() (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if f.ClientID > 0 {
		clauses = append(clauses, "e.client_id = ?")
		args = append(args, f.ClientID)
	}
	if f.InboundID > 0 {
		clauses = append(clauses, "e.inbound_id = ?")
		args = append(args, f.InboundID)
	}
	if f.Outcome != "" {
		clauses = append(clauses, "e.outcome = ?")
		args = append(args, f.Outcome)
	}
	if f.Network != "" {
		clauses = append(clauses, "e.network = ?")
		args = append(args, f.Network)
	}
	if f.Query != "" {
		clauses = append(clauses, `e.host LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Query)+"%")
	}
	if f.Since > 0 {
		clauses = append(clauses, "e.started_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		clauses = append(clauses, "e.started_at < ?")
		args = append(args, f.Until)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// AccessSummaryFilter selects the window of the aggregated overview.
type AccessSummaryFilter struct {
	ClientID  int64
	InboundID int64
	// SinceDay is the first day of the window (inclusive, YYYY-MM-DD).
	SinceDay string
	// TopLimit bounds the top-hosts and top-clients lists.
	TopLimit int
}

func (f AccessSummaryFilter) normalized() AccessSummaryFilter {
	if f.TopLimit <= 0 {
		f.TopLimit = accessTopDefault
	}
	if f.TopLimit > accessTopMax {
		f.TopLimit = accessTopMax
	}
	return f
}

// where renders the aggregate predicate shared by every summary query. prefix
// qualifies the columns for queries that join the client and inbound names in;
// the plain aggregate queries pass an empty prefix.
func (f AccessSummaryFilter) where(prefix string) (string, []any) {
	clauses := []string{prefix + "day >= ?"}
	args := []any{f.SinceDay}
	if f.ClientID > 0 {
		clauses = append(clauses, prefix+"client_id = ?")
		args = append(args, f.ClientID)
	}
	if f.InboundID > 0 {
		clauses = append(clauses, prefix+"inbound_id = ?")
		args = append(args, f.InboundID)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// SaveAccess persists a batch of access records: the raw events and the daily
// rollup in one transaction, so the overview can never describe a hit the
// event list does not have.
func (s *Store) SaveAccess(records []model.AccessRecord) error {
	if len(records) == 0 {
		return nil
	}
	return s.write(func(tx *sql.Tx) error {
		if err := insertAccessEvents(tx, records); err != nil {
			return err
		}
		return upsertAccessDaily(tx, records)
	})
}

// insertAccessEvents writes the raw rows with chunked multi-row statements.
func insertAccessEvents(tx *sql.Tx, records []model.AccessRecord) error {
	for start := 0; start < len(records); start += accessWriteChunk {
		end := start + accessWriteChunk
		if end > len(records) {
			end = len(records)
		}
		chunk := records[start:end]
		var b strings.Builder
		b.WriteString(`INSERT INTO access_events(client_id, inbound_id, network, host, port,
			ip, outcome, reason, up_bytes, down_bytes, started_at, duration_ms) VALUES `)
		args := make([]any, 0, len(chunk)*12)
		for i, record := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?,?,?,?,?,?,?,?,?,?,?)`)
			args = append(args, record.ClientID, record.InboundID, record.Network,
				record.Host, record.Port, record.IP, record.Outcome, record.Reason,
				record.UpBytes, record.DownBytes, record.StartedAt, record.DurationMS)
		}
		if _, err := tx.Exec(b.String(), args...); err != nil {
			return fmt.Errorf("save access events: %w", err)
		}
	}
	return nil
}

// accessDayKey is the rollup key of one aggregated access record. It carries
// only the dimensions the summary filters on: port, network and outcome are
// never filtered for the overview, so keeping them in the key would multiply
// the row count without answering a question.
type accessDayKey struct {
	day       string
	clientID  int64
	inboundID int64
	host      string
}

// upsertAccessDaily folds the batch into the daily rollup. Records without a
// day are stored as raw events only (the runtime always sets it).
func upsertAccessDaily(tx *sql.Tx, records []model.AccessRecord) error {
	type delta struct {
		key                          accessDayKey
		hits, allowed, blocked, fail int64
		up, down                     int64
	}
	byKey := make(map[accessDayKey]*delta, len(records))
	order := make([]*delta, 0, len(records))
	for _, record := range records {
		if record.Day == "" {
			continue
		}
		key := accessDayKey{
			day: record.Day, clientID: record.ClientID, inboundID: record.InboundID,
			host: record.Host,
		}
		d := byKey[key]
		if d == nil {
			d = &delta{key: key}
			byKey[key] = d
			order = append(order, d)
		}
		d.hits++
		switch record.Outcome {
		case model.AccessAllowed:
			d.allowed++
		case model.AccessBlocked:
			d.blocked++
		case model.AccessFailed:
			d.fail++
		}
		d.up += record.UpBytes
		d.down += record.DownBytes
	}
	for start := 0; start < len(order); start += accessWriteChunk {
		end := start + accessWriteChunk
		if end > len(order) {
			end = len(order)
		}
		chunk := order[start:end]
		var b strings.Builder
		b.WriteString(`INSERT INTO access_daily(day, client_id, inbound_id, host,
			hits, allowed, blocked, failed, up_bytes, down_bytes) VALUES `)
		args := make([]any, 0, len(chunk)*10)
		for i, d := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?,?,?,?,?,?,?,?,?)`)
			args = append(args, d.key.day, d.key.clientID, d.key.inboundID, d.key.host,
				d.hits, d.allowed, d.blocked, d.fail, d.up, d.down)
		}
		b.WriteString(` ON CONFLICT(day, client_id, inbound_id, host)
			DO UPDATE SET hits = hits + excluded.hits,
				allowed = allowed + excluded.allowed,
				blocked = blocked + excluded.blocked,
				failed = failed + excluded.failed,
				up_bytes = up_bytes + excluded.up_bytes,
				down_bytes = down_bytes + excluded.down_bytes`)
		if _, err := tx.Exec(b.String(), args...); err != nil {
			return fmt.Errorf("save access daily: %w", err)
		}
	}
	return nil
}

// ListAccessEvents returns one page of raw access records, newest first, plus
// the number matching the filter. The count stops at accessCountCap: rows past
// the offset cap cannot be paged to, and counting them only walks the index.
func (s *Store) ListAccessEvents(filter AccessEventFilter) ([]model.AccessEvent, int64, error) {
	filter = filter.normalized()
	where, args := filter.where()

	countArgs := append(append([]any{}, args...), accessCountCap)
	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM access_events e`+where+
		` LIMIT ?)`, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count access events: %w", err)
	}
	if total == 0 {
		return []model.AccessEvent{}, 0, nil
	}

	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.Query(`SELECT e.id, e.started_at, e.duration_ms, e.client_id,
		COALESCE(c.name, ''), e.inbound_id, COALESCE(i.remark, ''),
		e.network, e.host, e.port, e.ip, e.outcome, e.reason,
		e.up_bytes, e.down_bytes
		FROM access_events e
		LEFT JOIN clients c ON c.id = e.client_id
		LEFT JOIN inbounds i ON i.id = e.inbound_id`+where+`
		ORDER BY e.started_at DESC, e.id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := make([]model.AccessEvent, 0, filter.Limit)
	for rows.Next() {
		var event model.AccessEvent
		if err := rows.Scan(&event.ID, &event.StartedAt, &event.DurationMS,
			&event.ClientID, &event.ClientName, &event.InboundID, &event.InboundRemark,
			&event.Network, &event.Host, &event.Port, &event.IP, &event.Outcome,
			&event.Reason, &event.UpBytes, &event.DownBytes); err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

// AccessSummary builds the overview of a window from the daily rollup.
//
// The scans run concurrently. WAL allows several readers at once and the pool
// holds four connections, which matters because this driver (pure Go, no cgo) is
// about an order of magnitude slower than the C library on the same data:
// measured on a 90 day window over 360k rollup rows, the four scans cost 79 / 163
// / 284 / 248 ms one after another (774 ms total) but 310 ms when run together.
// A single backlog query must not add 0.8 s of latency to the page.
//
// The headline totals are nevertheless taken from the per-day rows the page
// already plots instead of a fifth scan: that saves a full pass and guarantees
// the cards and the trend cannot disagree.
func (s *Store) AccessSummary(filter AccessSummaryFilter) (model.AccessSummary, error) {
	filter = filter.normalized()
	if filter.SinceDay == "" {
		return model.AccessSummary{}, fmt.Errorf("access summary requires a start day")
	}
	where, args := filter.where("")
	clientWhere, clientArgs := filter.where("d.")

	var (
		daily      []model.AccessDayTotal
		topHosts   []model.AccessHostTotal
		topClients []model.AccessClientTotal
		unique     int64
		errs       [4]error
		wg         sync.WaitGroup
	)
	wg.Add(4)
	go func() {
		defer wg.Done()
		daily, errs[0] = s.scanAccessDaily(where, args)
	}()
	go func() {
		defer wg.Done()
		unique, errs[1] = s.scanAccessUniqueHosts(where, args)
	}()
	go func() {
		defer wg.Done()
		topHosts, errs[2] = s.scanAccessTopHosts(where, args, filter.TopLimit)
	}()
	go func() {
		defer wg.Done()
		topClients, errs[3] = s.scanAccessTopClients(clientWhere, clientArgs, filter.TopLimit)
	}()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return model.AccessSummary{}, err
		}
	}

	summary := model.AccessSummary{
		Daily:       daily,
		TopHosts:    topHosts,
		TopClients:  topClients,
		UniqueHosts: unique,
	}
	for _, day := range daily {
		summary.TotalHits += day.Hits
		summary.AllowedHits += day.AllowedHits
		summary.BlockedHits += day.BlockedHits
		summary.FailedHits += day.FailedHits
		summary.UpBytes += day.UpBytes
		summary.DownBytes += day.DownBytes
	}
	return summary, nil
}

// scanAccessUniqueHosts counts distinct destinations in the window. It cannot be
// derived from the per-day rows (the same host appears on several days), so it
// stays a separate aggregate.
func (s *Store) scanAccessUniqueHosts(where string, args []any) (int64, error) {
	var unique int64
	err := s.db.QueryRow(`SELECT COUNT(DISTINCT host) FROM access_daily`+where,
		args...).Scan(&unique)
	return unique, err
}

func (s *Store) scanAccessDaily(where string, args []any) ([]model.AccessDayTotal, error) {
	rows, err := s.db.Query(`SELECT day, SUM(hits), SUM(allowed), SUM(blocked),
			SUM(failed), SUM(up_bytes), SUM(down_bytes)
		FROM access_daily`+where+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make([]model.AccessDayTotal, 0, 32)
	for rows.Next() {
		var day model.AccessDayTotal
		if err := rows.Scan(&day.Day, &day.Hits, &day.AllowedHits, &day.BlockedHits,
			&day.FailedHits, &day.UpBytes, &day.DownBytes); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

func (s *Store) scanAccessTopHosts(where string, args []any, limit int) ([]model.AccessHostTotal, error) {
	pageArgs := append(append([]any{}, args...), limit)
	rows, err := s.db.Query(`SELECT host, SUM(hits), SUM(blocked),
			SUM(up_bytes), SUM(down_bytes)
		FROM access_daily`+where+`
		GROUP BY host ORDER BY SUM(hits) DESC, SUM(up_bytes + down_bytes) DESC, host
		LIMIT ?`, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := make([]model.AccessHostTotal, 0, limit)
	for rows.Next() {
		var host model.AccessHostTotal
		if err := rows.Scan(&host.Host, &host.Hits, &host.BlockedHits,
			&host.UpBytes, &host.DownBytes); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

func (s *Store) scanAccessTopClients(where string, args []any, limit int) ([]model.AccessClientTotal, error) {
	pageArgs := append(append([]any{}, args...), limit)
	rows, err := s.db.Query(`SELECT d.client_id, COALESCE(c.name, ''),
			d.inbound_id, COALESCE(i.remark, ''),
			SUM(d.hits), SUM(d.up_bytes), SUM(d.down_bytes)
		FROM access_daily d
		LEFT JOIN clients c ON c.id = d.client_id
		LEFT JOIN inbounds i ON i.id = d.inbound_id`+where+`
		GROUP BY d.client_id, d.inbound_id
		ORDER BY SUM(d.hits) DESC, SUM(d.up_bytes + d.down_bytes) DESC, d.client_id
		LIMIT ?`, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := make([]model.AccessClientTotal, 0, limit)
	for rows.Next() {
		var client model.AccessClientTotal
		if err := rows.Scan(&client.ClientID, &client.Name, &client.InboundID,
			&client.InboundRemark, &client.Hits, &client.UpBytes, &client.DownBytes); err != nil {
			return nil, err
		}
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

// PruneAccess removes raw events older than eventsBefore and rollup rows older
// than daysBefore. Both are inclusive lower bounds: a row is kept when it is
// not older than the cutoff.
//
// Deletion is bounded twice: every statement is limited to accessPruneBatch rows
// and every call stops after accessPruneBudget. Retention is a background chore,
// so it must neither hold the store write lock long enough to delay traffic
// accounting nor fall behind the rate at which records arrive: a tick that runs
// out of budget comes back on the next one, and the allowance per tick is far
// above what one instance produces.
func (s *Store) PruneAccess(eventsBefore int64, daysBefore string) (events, daily int64, err error) {
	deadline := time.Now().Add(accessPruneBudget)
	for time.Now().Before(deadline) {
		var n int64
		n, err = s.pruneAccessBatch(eventsBefore)
		events += n
		if err != nil {
			return events, daily, err
		}
		if n < accessPruneBatch {
			break
		}
	}
	for time.Now().Before(deadline) {
		var n int64
		n, err = s.pruneAccessDaysBatch(daysBefore)
		daily += n
		if err != nil {
			return events, daily, err
		}
		if n < accessPruneBatch {
			break
		}
	}
	return events, daily, nil
}

// pruneAccessBatch deletes one bounded batch of raw events. access_events has a
// rowid, so the batch is selected by id and the delete itself stays indexed.
func (s *Store) pruneAccessBatch(eventsBefore int64) (int64, error) {
	var deleted int64
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM access_events WHERE id IN (
			SELECT id FROM access_events WHERE started_at < ?
			ORDER BY started_at, id LIMIT ?)`,
			eventsBefore, accessPruneBatch)
		if err != nil {
			return err
		}
		deleted, err = res.RowsAffected()
		return err
	})
	return deleted, err
}

// pruneAccessDaysBatch deletes one bounded batch of rollup rows.
//
// The rollup is WITHOUT ROWID, so a batch is a range of primary keys instead of
// rowids: ordered by the key, the oldest day's rows come first, so a batch never
// spans more than one day unless that day is smaller than one batch. Deleting a
// whole day in one statement, by contrast, meant a single transaction with tens
// of thousands of rows and the write lock held for hundreds of milliseconds.
func (s *Store) pruneAccessDaysBatch(daysBefore string) (int64, error) {
	var deleted int64
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM access_daily
			WHERE (day, client_id, inbound_id, host) IN (
				SELECT day, client_id, inbound_id, host FROM access_daily
				WHERE day < ?
				ORDER BY day, client_id, inbound_id, host
				LIMIT ?)`,
			daysBefore, accessPruneBatch)
		if err != nil {
			return err
		}
		deleted, err = res.RowsAffected()
		return err
	})
	return deleted, err
}

// FoldAccessHosts bounds how many distinct destinations one (day, client,
// inbound) key keeps in the rollup.
//
// The host is whatever the client asked for, so a client rotating subdomains
// could otherwise create rows without limit for the whole retention window and
// every summary would slow down with them. Overflow is folded into a single
// model.AccessOtherHost row, so the hit and byte totals stay complete while the
// key's cardinality stays bounded.
//
// Only keys already over cap are touched, at most maxKeys per call, and each key
// is folded in its own transaction: like retention, this must not hold the write
// lock for long.
func (s *Store) FoldAccessHosts(sinceDay string, cap, maxKeys int) (folded int64, err error) {
	if cap <= 0 {
		cap = model.AccessMaxHostsPerKey
	}
	if maxKeys <= 0 {
		maxKeys = accessFoldMaxKeys
	}
	type foldKey struct {
		day       string
		clientID  int64
		inboundID int64
	}
	keys, err := s.accessOverflowKeys(sinceDay, cap, maxKeys)
	if err != nil || len(keys) == 0 {
		return 0, err
	}
	for _, key := range keys {
		n, err := s.foldAccessKey(key.day, key.clientID, key.inboundID, cap)
		folded += n
		if err != nil {
			return folded, err
		}
	}
	return folded, nil
}

// accessOverflowKeys lists the rollup keys that hold more than cap destinations
// on or after sinceDay.
func (s *Store) accessOverflowKeys(sinceDay string, cap, maxKeys int) ([]struct {
	day       string
	clientID  int64
	inboundID int64
}, error) {
	rows, err := s.db.Query(`SELECT day, client_id, inbound_id FROM access_daily
		WHERE day >= ? AND host <> ?
		GROUP BY day, client_id, inbound_id
		HAVING COUNT(*) > ?
		ORDER BY day DESC, COUNT(*) DESC
		LIMIT ?`,
		sinceDay, model.AccessOtherHost, cap, maxKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []struct {
		day       string
		clientID  int64
		inboundID int64
	}
	for rows.Next() {
		var key struct {
			day       string
			clientID  int64
			inboundID int64
		}
		if err := rows.Scan(&key.day, &key.clientID, &key.inboundID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// keptHosts is the subquery of the destinations a key keeps: the most visited
// cap of them, ties broken by name so the same set is computed by both the
// aggregate and the delete. The overflow row is excluded by the caller.
const keptHosts = `(SELECT host FROM access_daily
	WHERE day = ? AND client_id = ? AND inbound_id = ? AND host <> ?
	ORDER BY hits DESC, host LIMIT ?)`

// foldAccessKey folds one rollup key's overflow into the shared "<other>" row in
// one transaction, so the totals move from many rows to one without a window in
// which they are counted twice or not at all.
func (s *Store) foldAccessKey(day string, clientID, inboundID int64, cap int) (int64, error) {
	var folded int64
	err := s.write(func(tx *sql.Tx) error {
		var hits, allowed, blocked, failed, up, down, count int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(hits), 0), COALESCE(SUM(allowed), 0),
				COALESCE(SUM(blocked), 0), COALESCE(SUM(failed), 0),
				COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0), COUNT(*)
			FROM access_daily
			WHERE day = ? AND client_id = ? AND inbound_id = ? AND host <> ?
			AND host NOT IN `+keptHosts,
			day, clientID, inboundID, model.AccessOtherHost,
			day, clientID, inboundID, model.AccessOtherHost, cap,
		).Scan(&hits, &allowed, &blocked, &failed, &up, &down, &count); err != nil {
			return fmt.Errorf("sum folded hosts: %w", err)
		}
		if count == 0 {
			return nil
		}
		if _, err := tx.Exec(`INSERT INTO access_daily(day, client_id, inbound_id, host,
				hits, allowed, blocked, failed, up_bytes, down_bytes)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(day, client_id, inbound_id, host) DO UPDATE SET
				hits = hits + excluded.hits,
				allowed = allowed + excluded.allowed,
				blocked = blocked + excluded.blocked,
				failed = failed + excluded.failed,
				up_bytes = up_bytes + excluded.up_bytes,
				down_bytes = down_bytes + excluded.down_bytes`,
			day, clientID, inboundID, model.AccessOtherHost,
			hits, allowed, blocked, failed, up, down); err != nil {
			return fmt.Errorf("fold access hosts: %w", err)
		}
		res, err := tx.Exec(`DELETE FROM access_daily
			WHERE day = ? AND client_id = ? AND inbound_id = ? AND host <> ?
			AND host NOT IN `+keptHosts,
			day, clientID, inboundID, model.AccessOtherHost,
			day, clientID, inboundID, model.AccessOtherHost, cap)
		if err != nil {
			return fmt.Errorf("delete folded hosts: %w", err)
		}
		folded, err = res.RowsAffected()
		return err
	})
	return folded, err
}

// CountAccessEvents returns the number of stored raw events. It exists for
// diagnostics and tests, not for the panel.
func (s *Store) CountAccessEvents() (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM access_events`).Scan(&count)
	return count, err
}

// CountAccessDaily returns the number of stored rollup rows.
func (s *Store) CountAccessDaily() (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM access_daily`).Scan(&count)
	return count, err
}
