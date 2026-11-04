// Integration and differential tests for the transaction commands: MULTI,
// EXEC, DISCARD, WATCH and UNWATCH. Expected replies are Redis 8.4's.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// txScript runs on a single connection; WATCH conflicts that need a second
// client are covered by TestTx_WatchConflict.
var txScript = []testutil.Step{
	testutil.S("+OK", "MULTI"),
	testutil.S("+QUEUED", "SET", "a", "1"),
	testutil.S("+QUEUED", "INCR", "a"),
	testutil.S("+QUEUED", "GET", "a"),
	testutil.S(`*[+OK, :2, "2"]`, "EXEC"),
	testutil.S("-ERR EXEC without MULTI", "EXEC"),
	testutil.S("-ERR DISCARD without MULTI", "DISCARD"),
	testutil.S("+OK", "MULTI"),
	testutil.S("-ERR MULTI calls can not be nested", "MULTI"),
	testutil.S("+QUEUED", "SET", "b", "1"),
	testutil.S("+OK", "DISCARD"),
	testutil.S(":0", "EXISTS", "b"),
	// A command with bad arity aborts the whole transaction.
	testutil.S("+OK", "MULTI"),
	testutil.S("+QUEUED", "SET", "c", "1"),
	testutil.S("-ERR wrong number of arguments for 'get' command", "GET"),
	testutil.S("-EXECABORT Transaction discarded because of previous errors.", "EXEC"),
	testutil.S(":0", "EXISTS", "c"),
	// Unknown command also aborts.
	testutil.S("+OK", "MULTI"),
	testutil.S("-ERR unknown command 'NOSUCH', with args beginning with: ", "NOSUCH"),
	testutil.S("-EXECABORT Transaction discarded because of previous errors.", "EXEC"),
	// A runtime error is reported per command; the others still run.
	testutil.S("+OK", "SET", "s", "str"),
	testutil.S("+OK", "MULTI"),
	testutil.S("+QUEUED", "LPUSH", "s", "x"),
	testutil.S("+QUEUED", "SET", "d", "1"),
	testutil.S(`*[-WRONGTYPE Operation against a key holding the wrong kind of value, +OK]`, "EXEC"),
	testutil.S(`"1"`, "GET", "d"),
	// WATCH rules.
	testutil.S("+OK", "WATCH", "w"),
	testutil.S("+OK", "MULTI"),
	testutil.S("-ERR WATCH inside MULTI is not allowed", "WATCH", "w"),
	testutil.S("+OK", "DISCARD"),
	testutil.S("+OK", "UNWATCH"),
	testutil.S("+OK", "UNWATCH"),
	testutil.S("+OK", "WATCH", "w"),
	testutil.S("+OK", "MULTI"),
	testutil.S("+QUEUED", "PING"),
	testutil.S(`*[+PONG]`, "EXEC"),
	testutil.S(arityErr("watch"), "WATCH"),
	testutil.S(arityErr("multi"), "MULTI", "x"),
	testutil.S("+OK", "MULTI"),
	testutil.S("-ERR MULTI calls can not be nested", "MULTI"),
	testutil.S("+OK", "DISCARD"),
	testutil.S("+OK", "MULTI"),
	testutil.S(`*[]`, "EXEC"),
}

func TestTxCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), txScript)
}

func TestTxCommandsDifferential(t *testing.T) {
	testutil.Differential(t, txScript, testutil.KnownDivergences)
}

func TestTx_WatchConflict(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	a := testutil.MustDial(t, addr)
	b := testutil.MustDial(t, addr)
	testutil.RunScript(t, a, []testutil.Step{
		testutil.S("+OK", "SET", "k", "1"),
		testutil.S("+OK", "WATCH", "k"),
	})
	testutil.RunScript(t, b, []testutil.Step{testutil.S("+OK", "SET", "k", "2")})
	testutil.RunScript(t, a, []testutil.Step{
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "SET", "k", "3"),
		testutil.S("(nil-array)", "EXEC"),
		testutil.S(`"2"`, "GET", "k"),
		// The watch is cleared by EXEC, so a retry succeeds.
		testutil.S("+OK", "WATCH", "k"),
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "SET", "k", "4"),
		testutil.S("*[+OK]", "EXEC"),
		// UNWATCH drops the watch, so a concurrent write no longer aborts.
		testutil.S("+OK", "WATCH", "k"),
	})
	testutil.RunScript(t, b, []testutil.Step{testutil.S(":1", "DEL", "k")})
	testutil.RunScript(t, a, []testutil.Step{
		testutil.S("+OK", "UNWATCH"),
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "SET", "k", "5"),
		testutil.S("*[+OK]", "EXEC"),
		// Deleting a watched key aborts the transaction.
		testutil.S("+OK", "WATCH", "k"),
	})
	testutil.RunScript(t, b, []testutil.Step{testutil.S(":1", "DEL", "k")})
	testutil.RunScript(t, a, []testutil.Step{
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "PING"),
		testutil.S("(nil-array)", "EXEC"),
		// FLUSHALL touches every watched key.
		testutil.S("+OK", "WATCH", "z"),
	})
	testutil.RunScript(t, b, []testutil.Step{testutil.S("+OK", "FLUSHALL")})
	testutil.RunScript(t, a, []testutil.Step{
		testutil.S("+OK", "MULTI"),
		testutil.S("+QUEUED", "PING"),
		testutil.S("(nil-array)", "EXEC"),
	})
}
