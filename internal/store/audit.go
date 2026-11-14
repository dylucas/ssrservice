package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
)

// Bounds for listing the audit trail. They keep a single request from asking the
// database for an unbounded result set.
const (
	AuditDefaultLimit = 100
	AuditMaxLimit     = 500
)

// AuditFilter selects audit rows. A zero field means "no constraint".
type AuditFilter struct {
	Action   string
	Username string
	Since    int64 // inclusive, unix seconds
	Until    int64 // exclusive, unix seconds
	Limit    int
	Offset   int
}

// normalized applies the documented defaults and bounds.
func (f AuditFilter) normalized() AuditFilter {
	if f.Limit <= 0 {
		f.Limit = AuditDefaultLimit
	}
	if f.Limit > AuditMaxLimit {
		f.Limit = AuditMaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// where renders the filter predicate and its arguments.
func (f AuditFilter) where() (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if f.Action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, f.Action)
	}
	if f.Username != "" {
		clauses = append(clauses, "username = ?")
		args = append(args, f.Username)
	}
	if f.Since > 0 {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		clauses = append(clauses, "created_at < ?")
		args = append(args, f.Until)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// InsertAudit appends one audit row.
//
// It takes its own write transaction instead of joining a caller's: the store
// serializes writes with a single mutex, so nesting it inside another write
// transaction would deadlock. Callers record audit entries after the mutation
// committed, and a failure is only logged, never propagated to the request.
func (s *Store) InsertAudit(entry model.AuditLog) error {
	if entry.CreatedAt == 0 {
		entry.CreatedAt = model.NowUnix()
	}
	return s.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit_logs(created_at, admin_id, username, action,
			target_type, target_id, detail, ip) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.CreatedAt, entry.AdminID, entry.Username, entry.Action,
			entry.TargetType, entry.TargetID, entry.Detail, entry.IP)
		return err
	})
}

// ListAudit returns one page of audit rows, newest first, plus the total number
// of rows matching the filter.
func (s *Store) ListAudit(filter AuditFilter) ([]model.AuditLog, int64, error) {
	filter = filter.normalized()
	where, args := filter.where()

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit: %w", err)
	}
	if total == 0 {
		return []model.AuditLog{}, 0, nil
	}

	// LIMIT/OFFSET are appended after the filter arguments.
	pageArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := s.db.Query(`SELECT id, created_at, admin_id, username, action,
		target_type, target_id, detail, ip FROM audit_logs`+where+
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	logs := make([]model.AuditLog, 0, filter.Limit)
	for rows.Next() {
		var entry model.AuditLog
		if err := rows.Scan(&entry.ID, &entry.CreatedAt, &entry.AdminID, &entry.Username,
			&entry.Action, &entry.TargetType, &entry.TargetID, &entry.Detail, &entry.IP); err != nil {
			return nil, 0, err
		}
		logs = append(logs, entry)
	}
	return logs, total, rows.Err()
}

// PruneAudit deletes rows older than before and returns how many were removed.
func (s *Store) PruneAudit(before int64) (int64, error) {
	var removed int64
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM audit_logs WHERE created_at < ?`, before)
		if err != nil {
			return err
		}
		removed, err = res.RowsAffected()
		return err
	})
	return removed, err
}

// TrimAudit keeps at most max rows, deleting the oldest ones first. It returns
// how many rows were removed.
func (s *Store) TrimAudit(max int64) (int64, error) {
	if max <= 0 {
		return 0, nil
	}
	var removed int64
	err := s.write(func(tx *sql.Tx) error {
		var count int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&count); err != nil {
			return err
		}
		if count <= max {
			return nil
		}
		res, err := tx.Exec(`DELETE FROM audit_logs WHERE id IN (
			SELECT id FROM audit_logs ORDER BY id LIMIT ?)`, count-max)
		if err != nil {
			return err
		}
		removed, err = res.RowsAffected()
		return err
	})
	return removed, err
}
