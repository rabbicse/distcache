// Regression test: a subscriber that overflows its push queue is disconnected
// without taking the server down.
package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/testutil"
)

// TestPubSub_SlowSubscriberOverflowDoesNotCrash fills the push queue of a
// subscriber that never reads. The server must drop that client and keep
// serving; closing the queue twice used to panic the whole process.
func TestPubSub_SlowSubscriberOverflowDoesNotCrash(t *testing.T) {
	_, addr := testutil.StartServer(t, func(c *config.Config) { c.PubsubBufferLimit.Store(16) })

	sub := testutil.MustDial(t, addr)
	if err := sub.Send("SUBSCRIBE", "flood"); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Read(); err != nil {
		t.Fatal(err)
	}

	pub := testutil.MustDial(t, addr)
	payload := strings.Repeat("x", 64<<10)
	dropped := false
	for range 5000 {
		if pub.DoS("PUBLISH", "flood", payload) == ":0" {
			dropped = true
			break
		}
	}
	if !dropped {
		t.Fatal("subscriber was never disconnected for overflowing its push queue")
	}

	// Give the dropped client's teardown time to run, then check the server.
	time.Sleep(100 * time.Millisecond)
	if got := pub.DoS("PING"); got != "+PONG" {
		t.Fatalf("PING after overflow = %q, want +PONG", got)
	}
}
