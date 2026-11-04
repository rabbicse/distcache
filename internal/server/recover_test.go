package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/internal/testutil"
)

func TestHandlerPanicIsRecovered(t *testing.T) {
	s, addr := testutil.StartServer(t, nil)
	// No client is connected yet, so mutating the command table is race-free.
	s.RegisterTestCommand("boom", func(c *server.Ctx) { panic("kaboom") })

	c := testutil.MustDial(t, addr)
	if err := c.Send("BOOM", "k"); err != nil {
		t.Fatal(err)
	}
	if err := c.Send("PING"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"-ERR internal error", "+PONG"} {
		v, err := c.Read()
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		if got := testutil.Render(v, false); got != want {
			t.Errorf("reply %d: got %q, want %q", i, got, want)
		}
	}
	c2 := testutil.MustDial(t, addr)
	if got := c2.DoS("SET", "a", "1"); got != "+OK" {
		t.Errorf("SET on second connection: got %q, want +OK", got)
	}
	if got := c2.DoS("GET", "k"); got != "(nil)" {
		t.Errorf("GET k after panicking write: got %q, want (nil)", got)
	}
}
