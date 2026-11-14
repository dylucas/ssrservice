// Package logging configures the structured logger used across sspanel.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New creates a slog logger. The returned LevelVar can be used to change the
// level at runtime.
func New(level, format string) (*slog.Logger, *slog.LevelVar, error) {
	levelVar := &slog.LevelVar{}
	parsed, err := parseLevel(level)
	if err != nil {
		return nil, nil, err
	}
	levelVar.Set(parsed)

	opts := &slog.HandlerOptions{
		Level: levelVar,
	}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json", "":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		return nil, nil, fmt.Errorf("unsupported log format %q", format)
	}
	return slog.New(handler), levelVar, nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", level)
	}
}
