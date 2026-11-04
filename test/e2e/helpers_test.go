// Package e2e holds end-to-end tests that drive DistCache through a real
// client library (go-redis) and, for crash tests, a real server subprocess.
package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/redis/go-redis/v9"
)

// startNode starts an in-process DistCache on a random port with dir as its
// data directory and returns it with a connected go-redis client. extra may
// adjust the config. Both are closed on cleanup.
func startNode(t *testing.T, dir string, extra func(*config.Config)) (*server.Server, *redis.Client) {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = []string{"127.0.0.1"}
	cfg.Port = 0
	cfg.Dir = dir
	cfg.Save = nil
	if extra != nil {
		extra(cfg)
	}
	s := server.New(cfg, nil)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()
	select {
	case <-s.Ready():
	case err := <-errCh:
		t.Fatalf("server start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	rc := redis.NewClient(&redis.Options{Addr: s.Addr(), Protocol: 2})
	t.Cleanup(func() {
		_ = rc.Close()        // best-effort: teardown
		_ = s.Shutdown(false) // idempotent; tests may already have shut it down
	})
	return s, rc
}

// waitFor polls cond every 10 ms until it is true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// infoField returns field from an INFO section reply, or "" if absent.
func infoField(t *testing.T, rc *redis.Client, section, field string) string {
	t.Helper()
	info, err := rc.Info(context.Background(), section).Result()
	if err != nil {
		t.Fatalf("INFO %s: %v", section, err)
	}
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), field+":"); ok {
			return v
		}
	}
	return ""
}

// hostPort splits a 127.0.0.1:port address for REPLICAOF.
func hostPort(addr string) (string, string) {
	i := strings.LastIndexByte(addr, ':')
	return addr[:i], addr[i+1:]
}

// fileSize returns the size of path or fails the test.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Size()
}
