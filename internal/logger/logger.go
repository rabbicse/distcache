// Package logger configures structured logging (log/slog) for DistCache.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New returns a slog logger for a Redis style level name (debug, verbose,
// notice, warning, nothing) writing to path ("" or "stdout" for stdout).
func New(level, path string, json bool) (*slog.Logger, io.Closer, error) {
	var out io.Writer = os.Stdout
	var closer io.Closer = io.NopCloser(nil)
	if path != "" && path != "stdout" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, err
		}
		out, closer = f, f
	}
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "verbose":
		lv = slog.LevelDebug + 2
	case "warning":
		lv = slog.LevelWarn
	case "nothing":
		lv = slog.LevelError + 100
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if json {
		h = slog.NewJSONHandler(out, opts)
	} else {
		h = slog.NewTextHandler(out, opts)
	}
	return slog.New(h), closer, nil
}

// Discard returns a logger that drops everything (tests).
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 100}))
}
