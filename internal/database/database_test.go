package database

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestOpenRestrictsDatabasePermissions pins the promise made by
// docs/security.md: the database file is 0600.
//
// SQLite creates it with 0644 minus the umask, and the file holds every server
// PSK, client PSK, the password hash, the session signing key and every
// subscription token. The repository's own data/sspanel.db sat at 0644, so the
// documented guarantee was simply not implemented.
func TestOpenRestrictsDatabasePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database mode = %04o, want 0600", mode)
	}

	// Force the WAL sidecars into existence and check them too: they hold the
	// same rows until the next checkpoint.
	if _, err := db.Exec(`CREATE TABLE permission_probe(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Stat(sidecar)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", sidecar, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Fatalf("%s mode = %04o, want no group/other bits", sidecar, mode.Perm())
		}
	}
}

// TestRestrictFilesIgnoresMissingSidecars keeps the helper usable before the
// WAL files exist.
func TestRestrictFilesIgnoresMissingSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restrictFiles(path); err != nil {
		t.Fatalf("restrictFiles: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %04o, want 0600", mode)
	}
}

// TestAccessMigrationAppliesToAnExistingDatabase covers the upgrade path: a
// deployment created before the access analytics feature has an installed
// schema at version 7 and data in it, and reopening it must add the new tables,
// the rollup indexes and nothing else.
func TestAccessMigrationAppliesToAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upgrade.db")

	// Build a current database, then roll the access parts back out of it to
	// mimic one created before the feature existed.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`DROP INDEX IF EXISTS idx_access_daily_client`,
		`DROP INDEX IF EXISTS idx_access_daily_inbound`,
		`DROP TABLE IF EXISTS access_daily`,
		`DROP TABLE IF EXISTS access_events`,
		`DELETE FROM schema_migrations WHERE version >= 8`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Pre-existing data that the upgrade must not touch.
	hash := "$2a$04$012345678901234567890123456789012345678901234567890123"
	if _, err := db.Exec(`INSERT INTO admins(username, password_hash, must_change_password, created_at)
		VALUES('admin', ?, 0, 1)`, hash); err != nil {
		db.Close()
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbounds(remark, listen, port, method, server_psk, enabled, created_at, updated_at)
		VALUES('keep', '127.0.0.1', 8388, '2022-blake3-aes-128-gcm', 'psk', 1, 1, 1)`); err != nil {
		db.Close()
		t.Fatalf("seed inbound: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening runs the pending migrations; the data has to survive.
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var applied int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version >= 8`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 2 {
		t.Fatalf("%d access migrations recorded, want 2", applied)
	}
	for _, table := range []string{"access_events", "access_daily"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing after the upgrade: %v", table, err)
		}
	}
	for _, index := range []string{"idx_access_daily_client", "idx_access_daily_inbound"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name); err != nil {
			t.Fatalf("index %s missing after the upgrade: %v", index, err)
		}
	}
	var remark string
	if err := db.QueryRow(`SELECT remark FROM inbounds WHERE port = 8388`).Scan(&remark); err != nil {
		t.Fatalf("pre-existing inbound lost: %v", err)
	}
	if remark != "keep" {
		t.Fatalf("inbound remark = %q after the upgrade", remark)
	}

	// Running it again is a no-op: migrations are idempotent.
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count all migrations: %v", err)
	}
	if applied != 9 {
		t.Fatalf("%d migrations recorded, want 9", applied)
	}
}
