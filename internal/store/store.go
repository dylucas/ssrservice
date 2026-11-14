// Package store implements the persistence layer on top of SQLite.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ydong/ssrservice/internal/database"
)

// Sentinel errors returned by the store.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Store wraps the database handle and serializes write transactions.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// Open opens the database at path and returns a Store.
func Open(path string) (*Store, error) {
	db, err := database.Open(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the database handle, mainly for backups and diagnostics.
func (s *Store) DB() *sql.DB { return s.db }

// write runs fn inside a serialized write transaction.
func (s *Store) write(fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// scanErr normalizes database errors.
func scanErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if isConstraintErr(err) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func isConstraintErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "foreign key constraint failed") ||
		strings.Contains(msg, "constraint failed")
}

// BackupTo writes a consistent snapshot of the database to path.
func (s *Store) BackupTo(path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	escaped := strings.ReplaceAll(path, "'", "''")
	_, err := s.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", escaped))
	if err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}
	return nil
}

// Counts holds aggregate row counts used by the dashboard.
type Counts struct {
	Inbounds        int64
	Clients         int64
	EnabledInbounds int64
	EnabledClients  int64
}

// Stats returns aggregate counts.
func (s *Store) Stats() (Counts, error) {
	var counts Counts
	row := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM inbounds),
		(SELECT COUNT(*) FROM clients),
		(SELECT COUNT(*) FROM inbounds WHERE enabled = 1),
		(SELECT COUNT(*) FROM clients WHERE enabled = 1)`)
	if err := row.Scan(&counts.Inbounds, &counts.Clients, &counts.EnabledInbounds, &counts.EnabledClients); err != nil {
		return counts, err
	}
	return counts, nil
}
