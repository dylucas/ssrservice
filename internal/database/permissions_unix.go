//go:build !windows

package database

import (
	"fmt"
	"os"
	"syscall"
)

// restrictFiles makes the database and its WAL sidecars readable only by their
// owner.
//
// The file holds every server PSK, every client PSK, the bcrypt password hash,
// the session signing key and every subscription token, yet SQLite creates it
// with 0644 (minus the process umask). The deployment guide promises 0600, and
// a group-readable database is a real escalation path: any local account in the
// service group could read every credential. The modes are applied to the files
// that exist and re-applied after the WAL directory is created; a sidecar that
// appears later is covered by the umask set at startup.
//
// Errors are returned so the caller can decide: a read-only or foreign-owned
// database must not stop the service from starting.
func restrictFiles(path string) error {
	var errs []error
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(name, 0o600); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("chmod %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// restrictNewFiles sets the process umask so files this process creates are
// private by default.
//
// Without it, SQLite creates the WAL and shared-memory sidecars (and a fresh
// database) with the default umask, and those files carry the same
// credentials. 0077 also keeps future backups and temporary files private.
func restrictNewFiles() {
	_ = syscall.Umask(0o077)
}
