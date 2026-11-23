// Tests that protected mode is a live parameter and leaves loopback clients
// alone.
package server_test

import (
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

func TestProtectedMode_DefaultYesAndLoopbackAllowed(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	if got := c.DoS("CONFIG", "GET", "protected-mode"); !strings.Contains(got, `"yes"`) {
		t.Fatalf("got %s, want protected-mode yes", got)
	}
	if got := c.DoS("PING"); got != "+PONG" {
		t.Fatalf("got %s, want +PONG from a loopback client", got)
	}
	if got := c.DoS("CONFIG", "SET", "protected-mode", "no"); got != "+OK" {
		t.Fatalf("got %s, want +OK", got)
	}
	if got := c.DoS("CONFIG", "GET", "protected-mode"); !strings.Contains(got, `"no"`) {
		t.Fatalf("got %s, want protected-mode no", got)
	}
}
