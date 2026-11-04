// Integration tests for MONITOR: the formatted feed other clients' commands
// appear in. The monitor is an in-process session; commands come over TCP so
// the client address is real.
package server_test

import (
	"regexp"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// monitorLine matches "+<unix>.<micros> [<db> <addr>] <args...>" where %s is
// the already-quoted argument text.
func monitorLine(t *testing.T, addrRe, args string) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile(`^\+\d+\.\d{6} \[0 ` + addrRe + `\] ` + regexp.QuoteMeta(args) + `$`)
}

func TestMonitor_SeesOtherClientsCommands(t *testing.T) {
	s, addr := testutil.StartServer(t, nil)
	mon := s.NewSession()
	t.Cleanup(mon.Close)
	expectReply(t, mon, "+OK", "MONITOR")
	expectReply(t, mon, "+OK", "MONITOR") // idempotent

	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "SET", "k", "v"),
		testutil.S("+OK", "SET", "q", `a"b\c`),
		testutil.S(`"v"`, "GET", "k"),
	})
	re := `127\.0\.0\.1:\d+`
	for _, args := range []string{`"SET" "k" "v"`, `"SET" "q" "a\"b\\c"`, `"GET" "k"`} {
		got := sessionPush(t, mon)
		if !monitorLine(t, re, args).MatchString(got) {
			t.Fatalf("monitor line: got %s, want a line ending in %s", got, args)
		}
	}
	// Commands run inside a script are reported with the "lua" source.
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "EVAL", `return redis.call('set', 'x', '1')`, "0"),
	})
	// EVAL is flagged skip_monitor in Redis: only the script's own commands
	// are reported, with the "lua" source.
	if got := sessionPush(t, mon); !monitorLine(t, `lua`, `"set" "x" "1"`).MatchString(got) {
		t.Fatalf("script command line: got %s", got)
	}
	// Admin commands such as CONFIG are not echoed.
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "CONFIG", "SET", "maxmemory", "0"),
		testutil.S("+PONG", "PING"),
	})
	if got := sessionPush(t, mon); !monitorLine(t, re, `"PING"`).MatchString(got) {
		t.Fatalf("after CONFIG: got %s, want the PING line only", got)
	}
}

func TestMonitor_OverTCP(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	mon := testutil.MustDial(t, addr)
	testutil.RunScript(t, mon, []testutil.Step{testutil.S("+OK", "MONITOR")})
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":1", "INCR", "n")})
	v, err := mon.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.Render(v, false); !monitorLine(t, `127\.0\.0\.1:\d+`, `"INCR" "n"`).MatchString(got) {
		t.Fatalf("got %s, want an INCR line", got)
	}
}
