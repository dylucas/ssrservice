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
