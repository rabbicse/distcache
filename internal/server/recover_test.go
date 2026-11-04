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

func TestHandlerPanicAfterPartialReply(t *testing.T) {
	s, addr := testutil.StartServer(t, nil)
	s.RegisterTestCommand("halfboom", func(c *server.Ctx) {
		c.W.WriteArrayLen(3)
		panic("mid-reply")
	})
	c := testutil.MustDial(t, addr)
	for _, a := range [][]string{{"HALFBOOM", "k"}, {"PING"}} {
		if err := c.Send(a...); err != nil {
			t.Fatal(err)
		}
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
}

func TestHandlerPanicInsideMulti(t *testing.T) {
	s, addr := testutil.StartServer(t, nil)
	s.RegisterTestCommand("boom", func(c *server.Ctx) { panic("kaboom") })
	c := testutil.MustDial(t, addr)
	for _, a := range [][]string{{"MULTI"}, {"BOOM", "k"}, {"SET", "a", "1"}} {
		c.DoS(a...)
	}
	if got, want := c.DoS("EXEC"), "*[-ERR internal error, +OK]"; got != want {
		t.Errorf("EXEC: got %q, want %q", got, want)
	}
	if got := c.DoS("GET", "a"); got != `"1"` {
		t.Errorf("GET a: got %q, want \"1\"", got)
	}
	if got := c.DoS("PING"); got != "+PONG" {
		t.Errorf("PING: got %q, want +PONG", got)
	}
}

func TestHandlerPanicInsideEval(t *testing.T) {
	s, addr := testutil.StartServer(t, nil)
	s.RegisterTestCommand("boom", func(c *server.Ctx) { panic("kaboom") })
	c := testutil.MustDial(t, addr)
	got := c.DoS("EVAL", "return redis.call('BOOM', KEYS[1])", "1", "k")
	if len(got) == 0 || got[0] != '-' {
		t.Errorf("EVAL: got %q, want an error reply", got)
	}
	if got := c.DoS("PING"); got != "+PONG" {
		t.Errorf("PING: got %q, want +PONG", got)
	}
}
