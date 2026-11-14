// Package logging configures the structured logger used across sspanel.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New creates a slog logger and installs it as the process default.
//
// Installing it matters because a few packages (the database opener, which has
// no logger of its own) report through slog.Default(). Without this, those lines
// ignore the configured level and format and reach stderr as text even in a
// JSON-logging deployment.
//
// The returned LevelVar can be used to change the level at runtime.
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
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, levelVar, nil
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
