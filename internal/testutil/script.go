package testutil

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

// Step is one command in a script together with its expected reply.
type Step struct {
	// Cmd is the command and its arguments.
	Cmd []string
	// Want is the expected reply in Render form (for example "+OK", ":1",
	// "(nil)", "*[...]", "-ERR ..."). Differential ignores it.
	Want string
	// Sorted sorts the top-level elements of an aggregate reply before
	// comparison, for commands with unspecified order (SMEMBERS, KEYS, ...).
	Sorted bool
}

// S builds a Step expecting want from cmd.
func S(want string, cmd ...string) Step { return Step{Cmd: cmd, Want: want} }

// SS is like S but compares aggregate replies order-insensitively.
func SS(want string, cmd ...string) Step { return Step{Cmd: cmd, Want: want, Sorted: true} }

// covered holds the lowercase top-level command names executed by
// RunScript and Differential; it feeds the coverage gate. It is a sync.Map so
// parallel tests can record concurrently.
var covered sync.Map

// RecordCommands returns a function that records a command name (lowercased)
// as covered. RunScript and Differential use it internally; it is exported
// for tests that drive a Client directly and still want to count towards the
// coverage gate. t is accepted for symmetry with other helpers.
func RecordCommands(t testing.TB) func(name string) {
	t.Helper()
	return func(name string) { covered.Store(strings.ToLower(name), struct{}{}) }
}

// CoveredCommands returns the sorted lowercase names of all commands executed
// through RunScript or Differential so far in this process.
func CoveredCommands() []string {
	var out []string
	covered.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	slices.Sort(out)
	return out
}

// execStep runs one step on c, records its command as covered and returns the
// rendered reply. A transport error is rendered as "IOERR ...".
func execStep(t testing.TB, c *Client, i int, st Step) string {
	t.Helper()
	if len(st.Cmd) == 0 {
		t.Fatalf("step %d: empty command", i)
	}
	RecordCommands(t)(st.Cmd[0])
	v, err := c.Do(st.Cmd...)
	if err != nil {
		return ioErrPrefix + err.Error()
	}
	return Render(v, st.Sorted)
}

// ioErrPrefix marks a rendered transport failure (not a Redis error reply).
const ioErrPrefix = "IOERR "

// RunScript executes steps in order on c and fails t at the first step whose
// rendered reply differs from Want, with "step %d %v: got %s want %s".
// Steps after the mismatch are not run.
func RunScript(t testing.TB, c *Client, steps []Step) {
	t.Helper()
	for i, st := range steps {
		if got := execStep(t, c, i, st); got != st.Want {
			t.Fatalf("step %d %v: got %s want %s", i, st.Cmd, got, st.Want)
		}
	}
}
