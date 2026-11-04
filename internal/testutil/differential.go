package testutil

import (
	"strings"
	"testing"
)

// Divergence allowlists a command whose replies legitimately differ from
// Redis. Steps for that command still execute on both servers (so state stays
// in sync) but non-error replies are not compared; error replies always are.
type Divergence struct {
	// Cmd is the lowercase top-level command name.
	Cmd string
	// Reason says why the reply cannot match; it must be non-empty.
	Reason string
}

// KnownDivergences is the authoritative allowlist of commands whose replies
// are expected to differ from real Redis. Pass it (plus any test-specific
// entries) to Differential.
var KnownDivergences = []Divergence{
	{"info", "server-specific fields and values"},
	{"dump", "payload is an internal serialization format"},
	{"restore", "consumes DistCache's own DUMP payload format"},
	{"object", "internal encoding names differ"},
	{"debug", "implementation-specific subcommands"},
	{"memory", "memory accounting differs"},
	{"latency", "implementation-specific latency data"},
	{"lolwut", "version-art output differs"},
	{"module", "no module support"},
	{"command", "docs text differs"},
	{"client", "client list formatting differs"},
	{"config", "supported parameter set differs"},
	{"slowlog", "entry ids and timings differ"},
	{"role", "replication details differ"},
	{"replconf", "replication handshake details differ"},
	{"psync", "replication handshake details differ"},
	{"sync", "replication handshake details differ"},
	{"acl", "ACL details differ"},
	{"hello", "server name and version differ"},
}

// Differential runs steps (Want is ignored) against a fresh in-process
// DistCache and a real Redis, interleaved step by step so time-dependent
// replies (TTLs) do not drift, and fails if any step renders differently.
//
// Comparison rules: an allowlisted command (see Divergence) is exempt only
// when both replies are non-errors; if either reply is an error (starts with
// "-"), the full text must match even for allowlisted commands. Two different
// error strings therefore fail. A transport error on either side is fatal.
//
// If no Redis is available (see StartRedis) every step still runs on
// DistCache, which records command coverage and catches panics or hangs, but
// replies are not asserted and t is then skipped. Because it may use a shared
// external Redis, callers must not run it in parallel.
func Differential(t testing.TB, steps []Step, allow []Divergence) {
	t.Helper()
	redisAddr, ok := StartRedis(t)
	_, addr := StartServer(t, nil)
	dc := MustDial(t, addr)
	if !ok {
		for i, st := range steps {
			if got := execStep(t, dc, i, st); strings.HasPrefix(got, ioErrPrefix) {
				t.Fatalf("step %d %v: distcache transport error: %s", i, st.Cmd, got)
			}
		}
		t.Skip("redis-server not available (install it or set REDIS_SERVER_ADDR); ran steps on DistCache only")
	}
	rc := MustDial(t, redisAddr)
	gotDC := make([]string, len(steps))
	gotRedis := make([]string, len(steps))
	for i, st := range steps {
		gotDC[i] = execStep(t, dc, i, st)
		gotRedis[i] = execStep(t, rc, i, st)
		if strings.HasPrefix(gotDC[i], ioErrPrefix) || strings.HasPrefix(gotRedis[i], ioErrPrefix) {
			t.Fatalf("step %d %v: transport error: distcache %s, redis %s", i, st.Cmd, gotDC[i], gotRedis[i])
		}
	}
	CompareReplies(t, steps, gotDC, gotRedis, allow)
}

// CompareReplies is the pure, Redis-free comparison used by Differential:
// gotA is DistCache's rendered replies, gotB Redis's. It fails t at the first
// differing step. Allowlisted commands are exempt only when neither reply is
// an error; transport errors (IOERR) are fatal.
func CompareReplies(t testing.TB, steps []Step, gotA, gotB []string, allow []Divergence) {
	t.Helper()
	if len(gotA) != len(steps) || len(gotB) != len(steps) {
		t.Fatalf("reply count mismatch: %d steps, distcache %d, redis %d", len(steps), len(gotA), len(gotB))
	}
	allowed := make(map[string]bool, len(allow))
	for _, d := range allow {
		allowed[strings.ToLower(d.Cmd)] = true
	}
	for i, st := range steps {
		if len(st.Cmd) == 0 {
			continue
		}
		a, b := gotA[i], gotB[i]
		if strings.HasPrefix(a, ioErrPrefix) || strings.HasPrefix(b, ioErrPrefix) {
			t.Fatalf("step %d %v: transport error: distcache %s, redis %s", i, st.Cmd, a, b)
		}
		isErr := strings.HasPrefix(a, "-") || strings.HasPrefix(b, "-")
		if allowed[strings.ToLower(st.Cmd[0])] && !isErr {
			continue
		}
		if a != b {
			t.Fatalf("step %d %v: distcache %s, redis %s", i, st.Cmd, a, b)
		}
	}
}
