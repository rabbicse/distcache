// Regression test: XCLAIM and XAUTOCLAIM must replay from the AOF with the
// outcome they had when they ran, whatever the clock says at replay time.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// TestStream_ClaimsReplayWithOriginalOutcome runs claims whose min-idle-time
// decides the result, restarts from the AOF at a later clock time, and checks
// that the pending list is unchanged: entries that were too fresh to claim
// stay with their consumer, and claimed entries keep their owner and count.
func TestStream_ClaimsReplayWithOriginalOutcome(t *testing.T) {
	dir := t.TempDir()
	fc := testutil.NewFakeClock(time.Unix(1_700_000_000, 0))
	opts := testutil.Options{Clock: fc.Now, Mutate: func(c *config.Config) {
		c.Dir = dir
		c.AppendOnly.Store(true)
		c.AppendFsync = "always"
	}}

	s, addr := testutil.StartServerOpts(t, opts)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`"1-1"`, "XADD", "q", "1-1", "f", "v"),
		testutil.S(`"2-1"`, "XADD", "q", "2-1", "f", "v"),
		testutil.S(`"3-1"`, "XADD", "q", "3-1", "f", "v"),
		testutil.S("+OK", "XGROUP", "CREATE", "q", "g", "0"),
		testutil.S(`*[*["q", *[*["1-1", *["f", "v"]], *["2-1", *["f", "v"]], *["3-1", *["f", "v"]]]]]`, "XREADGROUP", "GROUP", "g", "owner", "STREAMS", "q", ">"),
	})
	fc.Advance(time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		// Idle for 1 s only: a 2 s threshold claims nothing.
		testutil.S("*[]", "XCLAIM", "q", "g", "thief", "2000", "1-1"),
		testutil.S(`*["0-0", *[], *[]]`, "XAUTOCLAIM", "q", "g", "thief", "2000", "0"),
		// A 500 ms threshold claims 3-1.
		testutil.S(`*["3-1"]`, "XCLAIM", "q", "g", "taker", "500", "3-1", "JUSTID"),
	})
	want := `*[*["1-1", "owner", :1000, :1], *["2-1", "owner", :1000, :1], *["3-1", "taker", :0, :1]]`
	if got := c.DoS("XPENDING", "q", "g", "-", "+", "10"); got != want {
		t.Fatalf("XPENDING before restart = %s, want %s", got, want)
	}
	if err := s.Shutdown(false); err != nil {
		t.Fatal(err)
	}

	// Replay 10 s later: every entry is now older than both thresholds.
	fc.Advance(10 * time.Second)
	_, addr = testutil.StartServerOpts(t, opts)
	c = testutil.MustDial(t, addr)
	want = `*[*["1-1", "owner", :11000, :1], *["2-1", "owner", :11000, :1], *["3-1", "taker", :10000, :1]]`
	if got := c.DoS("XPENDING", "q", "g", "-", "+", "10"); got != want {
		t.Errorf("XPENDING after AOF replay = %s, want %s", got, want)
	}
	// Consumers created by a claim that took nothing still exist after replay.
	if got, want := c.DoS("XINFO", "GROUPS", "q"), `*[*["name", "g", "consumers", :3, "pending", :3, "last-delivered-id", "3-1", "entries-read", :3, "lag", :0]]`; got != want {
		t.Errorf("XINFO GROUPS after AOF replay = %s, want %s", got, want)
	}
}
