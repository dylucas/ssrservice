//go:build windows

package database

// restrictFiles is a no-op on Windows: the POSIX mode bits do not exist and
// SQLite's files inherit the directory ACL instead.
func restrictFiles(string) error { return nil }

// restrictNewFiles is a no-op on Windows.
func restrictNewFiles() {}
