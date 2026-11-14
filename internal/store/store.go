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

// ConflictError is a constraint violation the caller can act on.
//
// The message is operator-facing: it names the field that collided (a client
// name inside an inbound, an inbound's listen address) without echoing the raw
// SQLite constraint text, which leaks the schema and table names into the API
// response.
type ConflictError struct {
	Message string
	Err     error
}

func (e *ConflictError) Error() string { return e.Message }
func (e *ConflictError) Unwrap() error { return e.Err }

// conflictErr builds an actionable message from a constraint failure.
func conflictErr(err error) error {
	msg := strings.ToLower(err.Error())
	var message string
	switch {
	case strings.Contains(msg, "clients.inbound_id, clients.name"),
		strings.Contains(msg, "clients.inbound_id, name"):
		message = "a client with this name already exists in the inbound"
	case strings.Contains(msg, "inbounds.listen, inbounds.port"),
		strings.Contains(msg, "inbounds.listen, port"):
		message = "another inbound already listens on this address and port"
	case strings.Contains(msg, "clients.sub_token"):
		message = "subscription token collision, please retry"
	case strings.Contains(msg, "admins.username"):
		message = "an administrator with this username already exists"
	case strings.Contains(msg, "foreign key"):
		message = "a referenced resource does not exist"
	default:
		message = "the resource conflicts with an existing one"
	}
	return &ConflictError{Message: message, Err: err}
}

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
		return fmt.Errorf("%w: %w", ErrConflict, conflictErr(err))
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
//
// VACUUM INTO reads the database inside one read transaction, so the snapshot is
// consistent on its own and does not need to hold the write lock: with WAL a
// reader never blocks a writer. Holding writeMu here used to stall every traffic
// flush and every panel write for the whole copy -- 95ms on an 11.5MB database,
// growing linearly with its size.
//
// The only thing this gives up is the guarantee that the snapshot contains the
// writes that were queued at request time; a backup is allowed to describe a
// consistent instant rather than the latest one.
func (s *Store) BackupTo(path string) error {
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
