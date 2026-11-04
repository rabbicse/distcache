// Integration and differential tests for the list command family, including
// the blocking variants. Expected replies are Redis 8.4's; the same script
// runs against real Redis in CI through testutil.Differential.
package server_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// listScript covers every list command with time-independent steps.
var listScript = concat(listBasicScript, listRemIndexScript, listPosScript, listMoveScript, listBlockingScript)

var listBasicScript = []testutil.Step{
	testutil.S(":3", "RPUSH", "l", "a", "b", "c"),
	testutil.S(":5", "LPUSH", "l", "y", "z"),
	testutil.S(`*["z", "y", "a", "b", "c"]`, "LRANGE", "l", "0", "-1"),
	testutil.S(":0", "LPUSHX", "nx", "a"),
	testutil.S(":0", "RPUSHX", "nx", "a"),
	testutil.S(":0", "EXISTS", "nx"),
	testutil.S(":6", "RPUSHX", "l", "d"),
	testutil.S(":7", "LPUSHX", "l", "w"),
	testutil.S(":7", "LLEN", "l"),
	testutil.S(":0", "LLEN", "missing"),
	testutil.S(`"w"`, "LPOP", "l"),
	testutil.S(`*["z", "y"]`, "LPOP", "l", "2"),
	testutil.S(`"d"`, "RPOP", "l"),
	testutil.S(`*["c", "b"]`, "RPOP", "l", "2"),
	testutil.S(`*["a"]`, "LPOP", "l", "10"),
	testutil.S(":0", "EXISTS", "l"),
	testutil.S("(nil)", "LPOP", "missing"),
	testutil.S("(nil-array)", "LPOP", "missing", "1"),
	testutil.S("(nil)", "RPOP", "missing"),
	testutil.S(":2", "RPUSH", "l", "a", "b"),
	testutil.S("*[]", "LPOP", "l", "0"),
	testutil.S("-ERR value is out of range, must be positive", "LPOP", "l", "-1"),
	testutil.S("-ERR value is out of range, must be positive", "RPOP", "l", "x"),
	testutil.S(`*["a", "b"]`, "LRANGE", "l", "-100", "100"),
	testutil.S(`*["b"]`, "LRANGE", "l", "-1", "-1"),
	testutil.S("*[]", "LRANGE", "l", "5", "10"),
	testutil.S("*[]", "LRANGE", "l", "1", "0"),
	testutil.S("*[]", "LRANGE", "missing", "0", "-1"),
	testutil.S(errNotInt, "LRANGE", "l", "0", "x"),
	testutil.S(":5", "RPUSH", "t", "1", "2", "3", "4", "5"),
	testutil.S("+OK", "LTRIM", "t", "1", "-2"),
	testutil.S(`*["2", "3", "4"]`, "LRANGE", "t", "0", "-1"),
	testutil.S("+OK", "LTRIM", "t", "5", "10"),
	testutil.S(":0", "EXISTS", "t"),
	testutil.S("+OK", "LTRIM", "missing", "0", "1"),
	testutil.S(errNotInt, "LTRIM", "l", "a", "1"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "LPUSH", "str", "a"),
	testutil.S(wrongType, "RPUSHX", "str", "a"),
	testutil.S(wrongType, "LPOP", "str"),
	testutil.S(wrongType, "LLEN", "str"),
	testutil.S(wrongType, "LRANGE", "str", "0", "-1"),
	testutil.S(wrongType, "LTRIM", "str", "0", "1"),
	testutil.S(arityErr("lpush"), "LPUSH", "l"),
	testutil.S(arityErr("rpush"), "RPUSH", "l"),
	testutil.S(arityErr("llen"), "LLEN"),
	testutil.S(arityErr("lrange"), "LRANGE", "l", "0"),
	testutil.S(arityErr("ltrim"), "LTRIM", "l", "0"),
	testutil.S(arityErr("lpop"), "LPOP", "l", "1", "2"),
}

// listRemIndexScript covers LREM count semantics, negative LINDEX/LSET
// indexes and LINSERT pivots.
var listRemIndexScript = []testutil.Step{
	testutil.S(":7", "RPUSH", "r", "a", "b", "c", "a", "b", "c", "a"),
	testutil.S(":2", "LREM", "r", "2", "a"),
	testutil.S(`*["b", "c", "b", "c", "a"]`, "LRANGE", "r", "0", "-1"),
	testutil.S(":1", "LREM", "r", "-1", "c"),
	testutil.S(`*["b", "c", "b", "a"]`, "LRANGE", "r", "0", "-1"),
	testutil.S(":2", "LREM", "r", "0", "b"),
	testutil.S(`*["c", "a"]`, "LRANGE", "r", "0", "-1"),
	testutil.S(":0", "LREM", "r", "0", "zz"),
	testutil.S(":0", "LREM", "missing", "0", "a"),
	testutil.S(errNotInt, "LREM", "r", "x", "a"),
	testutil.S(wrongType, "LREM", "str", "0", "a"),
	testutil.S(":4", "RPUSH", "r", "b", "d"),
	testutil.S(`"c"`, "LINDEX", "r", "0"),
	testutil.S(`"d"`, "LINDEX", "r", "-1"),
	testutil.S(`"c"`, "LINDEX", "r", "-4"),
	testutil.S("(nil)", "LINDEX", "r", "4"),
	testutil.S("(nil)", "LINDEX", "r", "-5"),
	testutil.S("(nil)", "LINDEX", "missing", "0"),
	testutil.S(errNotInt, "LINDEX", "r", "x"),
	testutil.S(wrongType, "LINDEX", "str", "0"),
	testutil.S("+OK", "LSET", "r", "-1", "D"),
	testutil.S("+OK", "LSET", "r", "0", "C"),
	testutil.S("+OK", "LSET", "r", "-3", "A"),
	testutil.S("-ERR index out of range", "LSET", "r", "4", "x"),
	testutil.S("-ERR index out of range", "LSET", "r", "-5", "x"),
	testutil.S("-ERR no such key", "LSET", "missing", "0", "x"),
	testutil.S(wrongType, "LSET", "str", "0", "x"),
	testutil.S(`*["C", "A", "b", "D"]`, "LRANGE", "r", "0", "-1"),
	testutil.S(":5", "LINSERT", "r", "BEFORE", "A", "x"),
	testutil.S(":6", "LINSERT", "r", "after", "D", "y"),
	testutil.S(":7", "LINSERT", "r", "AFTER", "C", "z"),
	testutil.S(`*["C", "z", "x", "A", "b", "D", "y"]`, "LRANGE", "r", "0", "-1"),
	testutil.S(":-1", "LINSERT", "r", "BEFORE", "nope", "q"),
	testutil.S(":0", "LINSERT", "missing", "BEFORE", "a", "q"),
	testutil.S(errSyntax, "LINSERT", "r", "MIDDLE", "A", "q"),
	testutil.S(wrongType, "LINSERT", "str", "BEFORE", "a", "q"),
	testutil.S(arityErr("linsert"), "LINSERT", "r", "BEFORE", "A"),
}

var listPosScript = []testutil.Step{
	testutil.S(":8", "RPUSH", "p", "a", "b", "c", "1", "2", "3", "c", "c"),
	testutil.S(":2", "LPOS", "p", "c"),
	testutil.S(":6", "LPOS", "p", "c", "RANK", "2"),
	testutil.S(":7", "LPOS", "p", "c", "RANK", "-1"),
	testutil.S(":6", "LPOS", "p", "c", "RANK", "-2"),
	testutil.S("(nil)", "LPOS", "p", "c", "RANK", "4"),
	testutil.S(`*[:2, :6]`, "LPOS", "p", "c", "COUNT", "2"),
	testutil.S(`*[:2, :6, :7]`, "LPOS", "p", "c", "COUNT", "0"),
	testutil.S(`*[:7, :6]`, "LPOS", "p", "c", "RANK", "-1", "COUNT", "2"),
	testutil.S(`*[:6, :7]`, "LPOS", "p", "c", "RANK", "2", "COUNT", "0"),
	testutil.S("*[]", "LPOS", "p", "c", "COUNT", "0", "MAXLEN", "1"),
	testutil.S(`*[:2]`, "LPOS", "p", "c", "COUNT", "0", "MAXLEN", "3"),
	testutil.S("(nil)", "LPOS", "p", "c", "MAXLEN", "2"),
	testutil.S(":7", "LPOS", "p", "c", "RANK", "-1", "MAXLEN", "1"),
	testutil.S("(nil)", "LPOS", "p", "zz"),
	testutil.S("*[]", "LPOS", "p", "zz", "COUNT", "1"),
	testutil.S("(nil)", "LPOS", "missing", "c"),
	testutil.S("-ERR RANK can't be zero: use 1 to start from the first match, 2 from the second ... or use negative to start from the end of the list", "LPOS", "p", "c", "RANK", "0"),
	testutil.S("-ERR COUNT can't be negative", "LPOS", "p", "c", "COUNT", "-1"),
	testutil.S("-ERR MAXLEN can't be negative", "LPOS", "p", "c", "MAXLEN", "-1"),
	testutil.S(errSyntax, "LPOS", "p", "c", "FOO", "1"),
	testutil.S(errNotInt, "LPOS", "p", "c", "RANK", "x"),
	testutil.S(wrongType, "LPOS", "str", "c"),
	testutil.S(arityErr("lpos"), "LPOS", "p"),
}

var listMoveScript = []testutil.Step{
	testutil.S(":3", "RPUSH", "src", "a", "b", "c"),
	testutil.S(`"a"`, "LMOVE", "src", "dst", "LEFT", "RIGHT"),
	testutil.S(`"c"`, "LMOVE", "src", "dst", "RIGHT", "LEFT"),
	testutil.S(`*["c", "a"]`, "LRANGE", "dst", "0", "-1"),
	testutil.S("(nil)", "LMOVE", "missing", "dst", "LEFT", "LEFT"),
	testutil.S(errSyntax, "LMOVE", "src", "dst", "UP", "LEFT"),
	testutil.S(wrongType, "LMOVE", "src", "str", "LEFT", "LEFT"),
	testutil.S(wrongType, "LMOVE", "str", "dst", "LEFT", "LEFT"),
	testutil.S(`"b"`, "RPOPLPUSH", "src", "dst"),
	testutil.S(":0", "EXISTS", "src"),
	testutil.S(`*["b", "c", "a"]`, "LRANGE", "dst", "0", "-1"),
	testutil.S(`"a"`, "RPOPLPUSH", "dst", "dst"),
	testutil.S(`*["a", "b", "c"]`, "LRANGE", "dst", "0", "-1"),
	testutil.S("(nil)", "RPOPLPUSH", "missing", "dst"),
	testutil.S(arityErr("rpoplpush"), "RPOPLPUSH", "dst"),
	testutil.S(`*["dst", *["a"]]`, "LMPOP", "2", "missing", "dst", "LEFT"),
	testutil.S(`*["dst", *["c", "b"]]`, "LMPOP", "1", "dst", "RIGHT", "COUNT", "5"),
	testutil.S(":0", "EXISTS", "dst"),
	testutil.S("(nil-array)", "LMPOP", "1", "missing", "LEFT"),
	testutil.S("-ERR numkeys should be greater than 0", "LMPOP", "0", "x", "LEFT"),
	testutil.S(errSyntax, "LMPOP", "1", "x", "MIDDLE"),
	testutil.S("-ERR count should be greater than 0", "LMPOP", "1", "x", "LEFT", "COUNT", "0"),
	testutil.S(wrongType, "LMPOP", "1", "str", "LEFT"),
}

// listBlockingScript only uses blocking commands that return without
// blocking (data present or a tiny timeout), so replies are deterministic.
var listBlockingScript = []testutil.Step{
	testutil.S(":2", "RPUSH", "b1", "x", "y"),
	testutil.S(`*["b1", "x"]`, "BLPOP", "missing", "b1", "0"),
	testutil.S(`*["b1", "y"]`, "BRPOP", "b1", "0"),
	testutil.S("(nil-array)", "BLPOP", "b1", "0.01"),
	testutil.S("(nil-array)", "BRPOP", "b1", "0.01"),
	testutil.S("-ERR timeout is negative", "BLPOP", "b1", "-1"),
	testutil.S("-ERR timeout is not a float or out of range", "BLPOP", "b1", "abc"),
	testutil.S(wrongType, "BLPOP", "str", "0"),
	testutil.S(arityErr("blpop"), "BLPOP", "b1"),
	testutil.S(":2", "RPUSH", "b2", "p", "q"),
	testutil.S(`"p"`, "BLMOVE", "b2", "b3", "LEFT", "RIGHT", "0"),
	testutil.S(`"q"`, "BRPOPLPUSH", "b2", "b3", "0"),
	testutil.S(`*["b3", *["q", "p"]]`, "BLMPOP", "0", "1", "b3", "LEFT", "COUNT", "2"),
	testutil.S("(nil-array)", "BLMPOP", "0.01", "1", "b3", "LEFT"),
	testutil.S("-ERR timeout is negative", "BLMPOP", "-1", "1", "b3", "LEFT"),
}

func TestListCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), listScript)
}

func TestListCommandsDifferential(t *testing.T) {
	testutil.Differential(t, listScript, testutil.KnownDivergences)
}

// waitBlocked polls INFO clients until exactly n clients are blocked.
func waitBlocked(t *testing.T, c *testutil.Client, n int) {
	t.Helper()
	want := "blocked_clients:" + strconv.Itoa(n) + "\r\n"
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, err := c.Do("INFO", "clients")
		if err != nil {
			t.Fatalf("INFO clients: %v", err)
		}
		if strings.Contains(string(v.Str), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("got INFO %q, want it to contain %q", v.Str, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// sendBlocking records cmd as covered and sends it on c without reading.
func sendBlocking(t *testing.T, c *testutil.Client, cmd ...string) {
	t.Helper()
	testutil.RecordCommands(t)(cmd[0])
	if err := c.Send(cmd...); err != nil {
		t.Fatalf("send %v: %v", cmd, err)
	}
}

// readReply reads one rendered reply from c and compares it to want.
func readReply(t *testing.T, c *testutil.Client, want string) {
	t.Helper()
	v, err := c.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := testutil.Render(v, false); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// blockingFIFOCase blocks two clients on cmd, then runs wake on a third and
// expects the clients to be served in the order they blocked.
type blockingFIFOCase struct {
	name          string
	setup         []testutil.Step
	cmd           []string
	wake          []string
	first, second string
}

// runBlockingFIFO runs one blockingFIFOCase on a fresh server.
func runBlockingFIFO(t *testing.T, tc blockingFIFOCase) {
	t.Helper()
	_, addr := testutil.StartServer(t, nil)
	ctl := testutil.MustDial(t, addr)
	c1, c2 := testutil.MustDial(t, addr), testutil.MustDial(t, addr)
	testutil.RunScript(t, ctl, tc.setup)
	sendBlocking(t, c1, tc.cmd...)
	waitBlocked(t, ctl, 1)
	sendBlocking(t, c2, tc.cmd...)
	waitBlocked(t, ctl, 2)
	if _, err := ctl.Do(tc.wake...); err != nil {
		t.Fatalf("%v: %v", tc.wake, err)
	}
	readReply(t, c1, tc.first)
	readReply(t, c2, tc.second)
}

// Redis serves clients blocked on a key in the order they blocked.
func TestListCommands_BlockingFIFO(t *testing.T) {
	tests := []blockingFIFOCase{
		{
			name:  "BLPOP",
			cmd:   []string{"BLPOP", "q", "0"},
			wake:  []string{"RPUSH", "q", "a", "b"},
			first: `*["q", "a"]`, second: `*["q", "b"]`,
		},
		{
			name:  "BLMOVE",
			cmd:   []string{"BLMOVE", "q", "d", "LEFT", "RIGHT", "0"},
			wake:  []string{"RPUSH", "q", "a", "b"},
			first: `"a"`, second: `"b"`,
		},
		{
			name:  "BLMPOP",
			cmd:   []string{"BLMPOP", "0", "1", "q", "LEFT"},
			wake:  []string{"RPUSH", "q", "a", "b"},
			first: `*["q", *["a"]]`, second: `*["q", *["b"]]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runBlockingFIFO(t, tc) })
	}
}

func TestListCommands_BlockingWakeAndTimeout(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c, other := testutil.MustDial(t, addr), testutil.MustDial(t, addr)
	sendBlocking(t, c, "BLPOP", "q", "0")
	waitBlocked(t, other, 1)
	testutil.RunScript(t, other, []testutil.Step{testutil.S(":1", "LPUSH", "q", "v")})
	readReply(t, c, `*["q", "v"]`)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("(nil-array)", "BLPOP", "q", "0.1"),
		testutil.S(":0", "EXISTS", "q"),
	})
}

func TestListCommands_ClientUnblockError(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c, other := testutil.MustDial(t, addr), testutil.MustDial(t, addr)
	id := c.DoS("CLIENT", "ID")
	sendBlocking(t, c, "BLPOP", "q", "0")
	waitBlocked(t, other, 1)
	testutil.RunScript(t, other, []testutil.Step{
		testutil.S(":1", "CLIENT", "UNBLOCK", strings.TrimPrefix(id, ":"), "ERROR"),
	})
	readReply(t, c, "-UNBLOCKED client unblocked via CLIENT UNBLOCK")
	waitBlocked(t, other, 0)
}
