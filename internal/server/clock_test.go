// Tests that expiry follows the injectable clock rather than wall time.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

func startWithClock(t *testing.T, mutate func(*config.Config)) (*testutil.FakeClock, *testutil.Client) {
	t.Helper()
	fc := testutil.NewFakeClock(time.Unix(1_700_000_000, 0))
	_, addr := testutil.StartServerOpts(t, testutil.Options{Mutate: mutate, Clock: fc.Now})
	c := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = c.Close() })
	return fc, c
}

func TestLazyExpiryUsesInjectedClock(t *testing.T) {
	fc, c := startWithClock(t, func(cfg *config.Config) { cfg.ActiveExpire.Store(false) })
	if got := c.DoS("SET", "k", "v", "PX", "100"); got != "+OK" {
		t.Fatalf("SET: got %q, want +OK", got)
	}
	fc.Advance(50 * time.Millisecond)
	if got := c.DoS("GET", "k"); got != `"v"` {
		t.Fatalf("GET after 50ms: got %q, want %q", got, `"v"`)
	}
	fc.Advance(60 * time.Millisecond)
	if got := c.DoS("GET", "k"); got != "(nil)" {
		t.Fatalf("GET after 110ms: got %q, want (nil)", got)
	}
	if got := c.DoS("TTL", "k"); got != ":-2" {
		t.Fatalf("TTL: got %q, want :-2", got)
	}
}

func TestActiveExpiryUsesInjectedClock(t *testing.T) {
	fc, c := startWithClock(t, func(cfg *config.Config) { cfg.Hz.Store(100) })
	if got := c.DoS("SET", "k", "v", "PX", "100"); got != "+OK" {
		t.Fatalf("SET: got %q, want +OK", got)
	}
	fc.Advance(200 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for {
		got := c.DoS("DBSIZE")
		if got == ":0" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("DBSIZE: got %q, want :0 within 1s", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClockJumpBackwardIsSafe(t *testing.T) {
	fc, c := startWithClock(t, nil)
	start := fc.Now()
	if got := c.DoS("SET", "k", "v", "PX", "100"); got != "+OK" {
		t.Fatalf("SET: got %q, want +OK", got)
	}
	fc.Set(start.Add(-time.Hour))
	time.Sleep(50 * time.Millisecond) // let active expiry run against the backward clock
	if got := c.DoS("GET", "k"); got != `"v"` {
		t.Fatalf("GET: got %q, want %q", got, `"v"`)
	}
}
