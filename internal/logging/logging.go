// Package logging builds the structured, leveled slog.Logger the commands use. It is
// the single place log handlers are configured, so every binary logs consistently. It
// imports only the standard library, so it never pulls graphics into the headless
// server and never leaks into the deterministic simulation.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel maps a level name to an slog.Level. An empty name is info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn, or error)", s)
	}
}

// ParseFormat validates a handler format name ("text" or "json"; empty is text).
func ParseFormat(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return "text", nil
	case "json":
		return "json", nil
	default:
		return "", fmt.Errorf("unknown log format %q (want text or json)", s)
	}
}

// New builds a logger writing to out at the given level in the given format. It returns
// a usage-style error for an unknown level or format so a command can surface it before
// starting work.
func New(out io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	format, err = ParseFormat(format)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(out, opts)
	default:
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h), nil
}
