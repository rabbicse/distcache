package testutil

import (
	"strings"
	"testing"
)

// Divergence allowlists a command whose replies legitimately differ from
// Redis. Steps for that command still execute on both servers (so state stays
// in sync) but their replies are not compared.
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
// DistCache and a real Redis, and fails if any non-allowlisted step renders
// differently. Two error replies are only equal if their full text matches.
// It skips t when no Redis is available (see StartRedis). Because it may use
// a shared external Redis, callers must not run it in parallel.
func Differential(t testing.TB, steps []Step, allow []Divergence) {
	t.Helper()
	redisAddr, ok := StartRedis(t)
	if !ok {
		t.Skip("redis-server not available (install it or set REDIS_SERVER_ADDR)")
	}
	_, addr := StartServer(t, nil)
	dc := MustDial(t, addr)
	rc := MustDial(t, redisAddr)
	gotDC := execSteps(t, dc, steps)
	gotRedis := execSteps(t, rc, steps)
	CompareReplies(t, steps, gotDC, gotRedis, allow)
}

// CompareReplies is the pure comparison used by Differential: gotA is DistCache's
// replies, gotB Redis's. It fails t at the first non-allowlisted step where
// they differ, including when both are errors with different text.
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
		if len(st.Cmd) == 0 || allowed[strings.ToLower(st.Cmd[0])] {
			continue
		}
		if gotA[i] != gotB[i] {
			t.Fatalf("step %d %v: distcache %s, redis %s", i, st.Cmd, gotA[i], gotB[i])
		}
	}
}
