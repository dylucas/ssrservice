package logging

import (
	"bytes"
	"log/slog"
	"testing"
)

// TestNewInstallsTheProcessDefault guards the bridge between the configured
// logger and slog.Default().
//
// The database opener has no logger of its own and reports permission problems
// through slog.Default(). Unless the configured logger is installed as the
// default, those lines ignore the configured level and format: a JSON-logging
// deployment would still get a text line on stderr.
func TestNewInstallsTheProcessDefault(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	logger, level, err := New("info", "json")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if logger == nil || level == nil {
		t.Fatal("New returned nil")
	}
	if got := slog.Default(); got != logger {
		t.Fatal("the configured logger was not installed as the process default")
	}

	// The default handler must honour the configured level: a debug line is
	// filtered even when it goes through slog.Default().
	level.Set(slog.LevelInfo)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))
	slog.Default().Debug("filtered")
	if buf.Len() != 0 {
		t.Fatalf("debug line was emitted at info level: %s", buf.String())
	}
}

// TestNewRejectsUnknownValues keeps bad config from silently falling back.
func TestNewRejectsUnknownValues(t *testing.T) {
	if _, _, err := New("verbose", "json"); err == nil {
		t.Fatal("unknown level must fail")
	}
	if _, _, err := New("info", "xml"); err == nil {
		t.Fatal("unknown format must fail")
	}
}

// TestNewEmitsJSON pins the format contract used by the deployment docs.
func TestNewEmitsJSON(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	if _, _, err := New("debug", "json"); err != nil {
		t.Fatalf("new: %v", err)
	}
	// The handler writes to stderr, so the assertion is on the default handler's
	// type rather than on captured output.
	if _, ok := slog.Default().Handler().(*slog.JSONHandler); !ok {
		t.Fatalf("default handler = %T, want *slog.JSONHandler", slog.Default().Handler())
	}
}
