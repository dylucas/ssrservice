// Package database opens the embedded SQLite database and applies migrations.
package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (and creates when missing) the SQLite database at path.
func Open(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	dsn := u.String() +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	// Set the umask before the first file is created so the database and its
	// WAL sidecars are never world-readable, even for the window between their
	// creation and the explicit chmod below.
	restrictNewFiles()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// A small pool is enough: writes are serialized by the store layer and
	// WAL allows concurrent readers.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	// The database carries every credential in the deployment, so it must not be
	// readable by other local accounts. A failure to tighten the mode is not
	// fatal -- a read-only or foreign-owned file must still open -- but it is
	// worth a warning: the deployment guide promises 0600.
	if err := restrictFiles(abs); err != nil {
		slog.Default().Warn("could not restrict database permissions", "path", abs, "error", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	// Re-apply after migration: the WAL sidecars are created on first write and
	// may not have existed when the first pass ran.
	if err := restrictFiles(abs); err != nil {
		slog.Default().Warn("could not restrict database permissions", "path", abs, "error", err)
	}
	return db, nil
}
