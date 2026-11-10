package logger

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLevelMapping(t *testing.T) {
	tests := []struct {
		level   string
		enabled []slog.Level
		off     []slog.Level
	}{
		{"debug", []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelError}, nil},
		{"verbose", []slog.Level{slog.LevelDebug + 2, slog.LevelInfo}, []slog.Level{slog.LevelDebug}},
		{"notice", []slog.Level{slog.LevelInfo, slog.LevelWarn}, []slog.Level{slog.LevelDebug + 2}},
		{"warning", []slog.Level{slog.LevelWarn, slog.LevelError}, []slog.Level{slog.LevelInfo}},
		{"nothing", nil, []slog.Level{slog.LevelWarn, slog.LevelError}},
		{"WARNING", []slog.Level{slog.LevelWarn}, []slog.Level{slog.LevelInfo}},
		{"bogus", []slog.Level{slog.LevelInfo}, []slog.Level{slog.LevelDebug}},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log.txt")
			l, closer, err := New(tc.level, path, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = closer.Close() })
			ctx := context.Background()
			for _, lv := range tc.enabled {
				if !l.Enabled(ctx, lv) {
					t.Errorf("level %v should be enabled", lv)
				}
			}
			for _, lv := range tc.off {
				if l.Enabled(ctx, lv) {
					t.Errorf("level %v should be disabled", lv)
				}
			}
		})
	}
}

func TestLogFileCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	l, closer, err := New("notice", path, true)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hello", "k", "v")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); !strings.Contains(s, `"msg":"hello"`) || !strings.Contains(s, `"k":"v"`) {
		t.Errorf("log contents %q", s)
	}
	// Reopening appends.
	l, closer, err = New("notice", path, false)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("second")
	_ = closer.Close() // best-effort in test
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "msg=second") {
		t.Errorf("append lost data: %q", data)
	}
}

func TestNew_StdoutAndErrors(t *testing.T) {
	for _, p := range []string{"", "stdout"} {
		l, closer, err := New("notice", p, false)
		if err != nil || l == nil {
			t.Fatalf("path %q: %v", p, err)
		}
		if err := closer.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}
	bad := filepath.Join(t.TempDir(), "no-such-dir", "x.log")
	if _, _, err := New("notice", bad, false); err == nil {
		t.Error("expected error for unwritable path")
	}
}

func TestDiscard(t *testing.T) {
	l := Discard()
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Error("Discard should drop errors")
	}
	l.Error("ignored")
}
