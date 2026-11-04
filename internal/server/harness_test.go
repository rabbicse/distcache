package server_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// recorder is a testing.TB that captures failures instead of failing the real
// test. Fatalf stops the calling goroutine via a panic that run recovers.
type recorder struct {
	testing.TB
	msgs []string
}

type stopRun struct{}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.msgs = append(r.msgs, fmt.Sprintf(f, a...)) }
func (r *recorder) Fatalf(f string, a ...any) { r.Errorf(f, a...); panic(stopRun{}) }
func (r *recorder) Fatal(a ...any)            { r.Fatalf("%s", fmt.Sprint(a...)) }
func (r *recorder) Cleanup(func())            {}
func (r *recorder) run(fn func(tb testing.TB)) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(stopRun); !ok {
				panic(p)
			}
		}
	}()
	fn(r)
}

func TestRunScript_ReportsFirstMismatch(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	r := &recorder{TB: t}
	r.run(func(tb testing.TB) {
		testutil.RunScript(tb, c, []testutil.Step{
			testutil.S("+OK", "SET", "k", "v"),
			testutil.S(`"nope"`, "GET", "k"),
			testutil.S("+OK", "SET", "never", "run"),
		})
	})
	want := `step 1 [GET k]: got "v" want "nope"`
	if len(r.msgs) != 1 || r.msgs[0] != want {
		t.Fatalf("got %q, want [%q]", r.msgs, want)
	}
	if got := c.DoS("EXISTS", "never"); got != ":0" {
		t.Fatalf("step after mismatch ran: EXISTS got %s", got)
	}
}

func TestRunScript_PassesAndRecordsCoverage(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "SeT", "k", "v"),
	})
	found := false
	for _, n := range testutil.CoveredCommands() {
		found = found || n == "set"
	}
	if !found {
		t.Fatalf("CoveredCommands() = %v, want it to contain \"set\"", testutil.CoveredCommands())
	}
}

func TestDistCache_GetNoArgsErrorText(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("-ERR wrong number of arguments for 'get' command", "GET"),
	})
}

// CompareReplies is the pure comparison behind Differential, so it needs no Redis.
func TestDifferentialDetectsBothErrors(t *testing.T) {
	steps := []testutil.Step{testutil.S("", "GET")}
	const redisErr = "-ERR wrong number of arguments for 'get' command"
	tests := []struct {
		name     string
		a, b     string
		allow    []testutil.Divergence
		wantFail bool
	}{
		{"identical errors pass", redisErr, redisErr, nil, false},
		{"different errors fail", "-ERR something else", redisErr, nil, true},
		{"allowlisted differing passes", "-ERR x", "-ERR y", []testutil.Divergence{{Cmd: "get", Reason: "test"}}, false},
		{"different values fail", `"a"`, `"b"`, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{TB: t}
			r.run(func(tb testing.TB) {
				testutil.CompareReplies(tb, steps, []string{tc.a}, []string{tc.b}, tc.allow)
			})
			if failed := len(r.msgs) > 0; failed != tc.wantFail {
				t.Fatalf("failed = %v (%q), want %v", failed, r.msgs, tc.wantFail)
			}
		})
	}
	t.Run("length mismatch fails", func(t *testing.T) {
		r := &recorder{TB: t}
		r.run(func(tb testing.TB) { testutil.CompareReplies(tb, steps, nil, nil, nil) })
		if len(r.msgs) == 0 {
			t.Fatal("want failure on reply count mismatch")
		}
	})
}

func TestKnownDivergences_WellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range testutil.KnownDivergences {
		if d.Cmd == "" || d.Cmd != strings.ToLower(d.Cmd) {
			t.Errorf("divergence %q: command must be non-empty lowercase", d.Cmd)
		}
		if strings.TrimSpace(d.Reason) == "" {
			t.Errorf("divergence %q: empty Reason", d.Cmd)
		}
		if seen[d.Cmd] {
			t.Errorf("divergence %q listed twice", d.Cmd)
		}
		seen[d.Cmd] = true
	}
	if len(seen) != 19 {
		t.Errorf("got %d divergences, want 19", len(seen))
	}
}

// TestDifferential_AgainstRealRedis skips when no Redis is available. It must
// not call t.Parallel: REDIS_SERVER_ADDR points at a shared server.
func TestDifferential_AgainstRealRedis(t *testing.T) {
	testutil.Differential(t, []testutil.Step{
		testutil.S("", "SET", "k", "v"),
		testutil.S("", "GET", "k"),
		testutil.S("", "GET"),
		testutil.S("", "INCR", "k"),
		testutil.S("", "RPUSH", "l", "a", "b"),
		testutil.S("", "LRANGE", "l", "0", "-1"),
		testutil.SS("", "KEYS", "*"),
	}, testutil.KnownDivergences)
}
