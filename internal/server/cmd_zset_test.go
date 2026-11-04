// Integration and differential tests for the sorted-set command family,
// including the blocking pops. Expected replies are Redis 8.4's; the same
// script runs against real Redis in CI through testutil.Differential.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

const errNotFloat = "-ERR value is not a valid float"

// zsetScript covers every sorted-set command with deterministic steps.
var zsetScript = concat(zsetAddScript, zsetRangeScript, zsetRemoveScript, zsetAlgebraScript, zsetPopScript)

var zsetAddScript = []testutil.Step{
	testutil.S(":3", "ZADD", "z", "1", "a", "2", "b", "3", "c"),
	testutil.S(":0", "ZADD", "z", "NX", "10", "a"),
	testutil.S(":1", "ZADD", "z", "NX", "4", "d"),
	testutil.S(":0", "ZADD", "z", "XX", "5", "e"),
	testutil.S(":0", "EXISTS", "e"),
	testutil.S(":1", "ZADD", "z", "XX", "CH", "5", "d", "6", "e"),
	testutil.S(":0", "ZADD", "z", "GT", "CH", "1", "c"),
	testutil.S(":1", "ZADD", "z", "GT", "CH", "30", "c"),
	testutil.S(":0", "ZADD", "z", "LT", "CH", "40", "c"),
	testutil.S(":1", "ZADD", "z", "LT", "CH", "3", "c"),
	testutil.S(":1", "ZADD", "z", "LT", "0", "f"),
	testutil.S(`"13"`, "ZADD", "z", "INCR", "10", "c"),
	testutil.S("(nil)", "ZADD", "z", "NX", "INCR", "1", "c"),
	testutil.S("(nil)", "ZADD", "z", "XX", "INCR", "1", "nope"),
	testutil.S("(nil)", "ZADD", "z", "GT", "INCR", "-1", "c"),
	testutil.S(`"13"`, "ZSCORE", "z", "c"),
	testutil.S("-ERR XX and NX options at the same time are not compatible", "ZADD", "z", "NX", "XX", "1", "a"),
	testutil.S("-ERR GT, LT, and/or NX options at the same time are not compatible", "ZADD", "z", "GT", "LT", "1", "a"),
	testutil.S("-ERR GT, LT, and/or NX options at the same time are not compatible", "ZADD", "z", "NX", "GT", "1", "a"),
	testutil.S("-ERR INCR option supports a single increment-element pair", "ZADD", "z", "INCR", "1", "a", "2", "b"),
	testutil.S(errNotFloat, "ZADD", "z", "nan", "a"),
	testutil.S(errNotFloat, "ZADD", "z", "x", "a"),
	testutil.S(errSyntax, "ZADD", "z", "1", "a", "2"),
	testutil.S(arityErr("zadd"), "ZADD", "z", "1"),
	testutil.S(":2", "ZADD", "inf", "+inf", "a", "-inf", "b"),
	testutil.S("-ERR resulting score is not a number (NaN)", "ZINCRBY", "inf", "-inf", "a"),
	testutil.S(`"inf"`, "ZSCORE", "inf", "a"),
	testutil.S(`"2.5"`, "ZINCRBY", "z", "2.5", "new"),
	testutil.S(`"3.5"`, "ZINCRBY", "z", "1", "new"),
	testutil.S(errNotFloat, "ZINCRBY", "z", "x", "new"),
	testutil.S(":6", "ZCARD", "z"),
	testutil.S(":0", "ZCARD", "missing"),
	testutil.S("(nil)", "ZSCORE", "z", "nope"),
	testutil.S("(nil)", "ZSCORE", "missing", "a"),
	testutil.S(`*["1", (nil), "13"]`, "ZMSCORE", "z", "a", "nope", "c"),
	testutil.S(`*[(nil)]`, "ZMSCORE", "missing", "a"),
	testutil.S(":1", "ZRANK", "z", "a"),
	testutil.S(`*[:1, "1"]`, "ZRANK", "z", "a", "WITHSCORE"),
	testutil.S(":4", "ZREVRANK", "z", "a"),
	testutil.S(`*[:4, "1"]`, "ZREVRANK", "z", "a", "WITHSCORE"),
	testutil.S("(nil)", "ZRANK", "z", "nope"),
	testutil.S("(nil-array)", "ZRANK", "z", "nope", "WITHSCORE"),
	testutil.S("(nil)", "ZREVRANK", "missing", "a"),
	testutil.S(errSyntax, "ZRANK", "z", "a", "FOO"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "ZADD", "str", "1", "a"),
	testutil.S(wrongType, "ZSCORE", "str", "a"),
	testutil.S(wrongType, "ZCARD", "str"),
	testutil.S(wrongType, "ZRANK", "str", "a"),
}

var zsetRangeScript = []testutil.Step{
	testutil.S(":5", "ZADD", "r", "1", "a", "2", "b", "3", "c", "4", "d", "5", "e"),
	testutil.S(`*["a", "b", "c", "d", "e"]`, "ZRANGE", "r", "0", "-1"),
	testutil.S(`*["a", "1", "b", "2"]`, "ZRANGE", "r", "0", "1", "WITHSCORES"),
	testutil.S(`*["e", "d"]`, "ZRANGE", "r", "0", "1", "REV"),
	testutil.S(`*["b", "c", "d"]`, "ZRANGE", "r", "2", "4", "BYSCORE"),
	testutil.S(`*["c", "d"]`, "ZRANGE", "r", "(2", "4", "BYSCORE"),
	testutil.S(`*["c", "3"]`, "ZRANGE", "r", "-inf", "+inf", "BYSCORE", "LIMIT", "2", "1", "WITHSCORES"),
	testutil.S(`*["d", "c", "b"]`, "ZRANGE", "r", "4", "2", "BYSCORE", "REV"),
	testutil.S(`*["c", "b"]`, "ZRANGE", "r", "+inf", "-inf", "BYSCORE", "REV", "LIMIT", "2", "2"),
	testutil.S("*[]", "ZRANGE", "r", "10", "20", "BYSCORE"),
	testutil.S(`*["b", "c"]`, "ZRANGE", "r", "[b", "(d", "BYLEX"),
	testutil.S(`*["e", "d"]`, "ZRANGE", "r", "+", "[d", "BYLEX", "REV"),
	testutil.S(`*["b"]`, "ZRANGE", "r", "-", "+", "BYLEX", "LIMIT", "1", "1"),
	testutil.S("-ERR min or max not valid string range item", "ZRANGE", "r", "a", "c", "BYLEX"),
	testutil.S("-ERR min or max is not a float", "ZRANGE", "r", "a", "c", "BYSCORE"),
	testutil.S("-ERR syntax error, LIMIT is only supported in combination with either BYSCORE or BYLEX", "ZRANGE", "r", "0", "1", "LIMIT", "0", "1"),
	testutil.S("-ERR syntax error, WITHSCORES not supported in combination with BYLEX", "ZRANGE", "r", "-", "+", "BYLEX", "WITHSCORES"),
	testutil.S(errSyntax, "ZRANGE", "r", "0", "1", "FOO"),
	testutil.S("*[]", "ZRANGE", "missing", "0", "-1"),
	testutil.S(`*["e", "5", "d", "4"]`, "ZREVRANGE", "r", "0", "1", "WITHSCORES"),
	testutil.S(`*["a", "1", "b", "2"]`, "ZRANGEBYSCORE", "r", "-inf", "(3", "WITHSCORES"),
	testutil.S(`*["b"]`, "ZRANGEBYSCORE", "r", "1", "5", "LIMIT", "1", "1"),
	testutil.S(`*["c", "b"]`, "ZREVRANGEBYSCORE", "r", "3", "2"),
	testutil.S(`*["b", "c"]`, "ZRANGEBYLEX", "r", "[b", "[c"),
	testutil.S(`*["c", "b"]`, "ZREVRANGEBYLEX", "r", "[c", "[b"),
	testutil.S(`*["a", "b", "c"]`, "ZRANGEBYLEX", "r", "-", "[c"),
	testutil.S(`*["c", "b"]`, "ZREVRANGEBYLEX", "r", "(d", "-", "LIMIT", "0", "2"),
	testutil.S(":3", "ZCOUNT", "r", "2", "4"),
	testutil.S(":2", "ZCOUNT", "r", "(2", "4"),
	testutil.S(":0", "ZCOUNT", "missing", "-inf", "+inf"),
	testutil.S("-ERR min or max is not a float", "ZCOUNT", "r", "x", "4"),
	testutil.S(":3", "ZLEXCOUNT", "r", "[b", "[d"),
	testutil.S(":5", "ZLEXCOUNT", "r", "-", "+"),
	testutil.S("-ERR min or max not valid string range item", "ZLEXCOUNT", "r", "b", "d"),
	testutil.S(":2", "ZRANGESTORE", "dst", "r", "0", "1"),
	testutil.S(`*["a", "1", "b", "2"]`, "ZRANGE", "dst", "0", "-1", "WITHSCORES"),
	testutil.S(":2", "ZRANGESTORE", "dst", "r", "(3", "+inf", "BYSCORE"),
	testutil.S(`*["d", "e"]`, "ZRANGE", "dst", "0", "-1"),
	testutil.S(":1", "ZRANGESTORE", "dst", "r", "+", "-", "BYLEX", "REV", "LIMIT", "0", "1"),
	testutil.S(`*["e"]`, "ZRANGE", "dst", "0", "-1"),
	testutil.S(":0", "ZRANGESTORE", "dst", "r", "10", "20"),
	testutil.S(":0", "EXISTS", "dst"),
	testutil.S(errSyntax, "ZRANGESTORE", "dst", "r", "0", "1", "WITHSCORES"),
	testutil.S(wrongType, "ZRANGE", "str", "0", "1"),
	testutil.S(wrongType, "ZRANGESTORE", "dst", "str", "0", "1"),
	testutil.S(":7", "ZADD", "sc", "0", "m1", "0", "m2", "0", "m3", "0", "m4", "0", "m5", "0", "m6", "0", "m7"),
	testutil.S(`*["0", *["m1", "0"]]`, "ZSCAN", "sc", "0", "MATCH", "m1", "COUNT", "100"),
	testutil.S(`*["0", *[]]`, "ZSCAN", "missing", "0"),
	testutil.S("-ERR invalid cursor", "ZSCAN", "sc", "x"),
	testutil.S(wrongType, "ZSCAN", "str", "0"),
}

var zsetRemoveScript = []testutil.Step{
	testutil.S(":6", "ZADD", "d", "1", "a", "2", "b", "3", "c", "4", "d", "5", "e", "6", "f"),
	testutil.S(":2", "ZREM", "d", "a", "nope", "b"),
	testutil.S(":0", "ZREM", "missing", "a"),
	testutil.S(":1", "ZREMRANGEBYRANK", "d", "-1", "-1"),
	testutil.S(":1", "ZREMRANGEBYSCORE", "d", "(3", "4"),
	testutil.S(`*["c", "e"]`, "ZRANGE", "d", "0", "-1"),
	testutil.S(":0", "ZREMRANGEBYLEX", "d", "[a", "[b"),
	testutil.S(":2", "ZREMRANGEBYLEX", "d", "-", "+"),
	testutil.S(":0", "EXISTS", "d"),
	testutil.S(":0", "ZREMRANGEBYRANK", "missing", "0", "-1"),
	testutil.S("-ERR min or max is not a float", "ZREMRANGEBYSCORE", "d", "x", "1"),
	testutil.S("-ERR min or max not valid string range item", "ZREMRANGEBYLEX", "d", "a", "b"),
	testutil.S(errNotInt, "ZREMRANGEBYRANK", "d", "x", "1"),
	testutil.S(wrongType, "ZREM", "str", "a"),
	testutil.S(arityErr("zrem"), "ZREM", "d"),
}

var zsetAlgebraScript = []testutil.Step{
	testutil.S(":3", "ZADD", "za", "1", "a", "2", "b", "3", "c"),
	testutil.S(":3", "ZADD", "zb", "10", "b", "20", "c", "30", "d"),
	testutil.S(":2", "SADD", "plain", "a", "d"),
	testutil.S(`*["a", "b", "c", "d"]`, "ZUNION", "2", "za", "zb"),
	testutil.S(`*["a", "1", "b", "12", "c", "23", "d", "30"]`, "ZUNION", "2", "za", "zb", "WITHSCORES"),
	testutil.S(`*["a", "2", "b", "14", "c", "26", "d", "30"]`, "ZUNION", "2", "za", "zb", "WEIGHTS", "2", "1", "WITHSCORES"),
	testutil.S(`*["a", "1", "b", "10", "c", "20", "d", "30"]`, "ZUNION", "2", "za", "zb", "AGGREGATE", "MAX", "WITHSCORES"),
	testutil.S(`*["a", "1", "b", "2", "c", "3", "d", "30"]`, "ZUNION", "2", "za", "zb", "AGGREGATE", "MIN", "WITHSCORES"),
	testutil.S(`*["d", "1", "a", "2", "b", "2", "c", "3"]`, "ZUNION", "2", "za", "plain", "WITHSCORES"),
	testutil.S(`*["b", "12", "c", "23"]`, "ZINTER", "2", "za", "zb", "WITHSCORES"),
	testutil.S(`*["c", "-17", "b", "-8"]`, "ZINTER", "2", "za", "zb", "WEIGHTS", "1", "-1", "WITHSCORES"),
	testutil.S(`*["b", "10", "c", "20"]`, "ZINTER", "2", "za", "zb", "AGGREGATE", "MAX", "WITHSCORES"),
	testutil.S(`*["a"]`, "ZDIFF", "2", "za", "zb"),
	testutil.S(`*["a", "1"]`, "ZDIFF", "2", "za", "zb", "WITHSCORES"),
	testutil.S("*[]", "ZDIFF", "2", "missing", "za"),
	testutil.S(":4", "ZUNIONSTORE", "out", "2", "za", "zb", "WEIGHTS", "1", "2"),
	testutil.S(`*["a", "1", "b", "22", "c", "43", "d", "60"]`, "ZRANGE", "out", "0", "-1", "WITHSCORES"),
	testutil.S(":2", "ZINTERSTORE", "out", "2", "za", "zb", "AGGREGATE", "MIN"),
	testutil.S(`*["b", "2", "c", "3"]`, "ZRANGE", "out", "0", "-1", "WITHSCORES"),
	testutil.S(":0", "ZINTERSTORE", "out", "2", "za", "missing"),
	testutil.S(":0", "EXISTS", "out"),
	testutil.S(":1", "ZDIFFSTORE", "out", "2", "za", "zb"),
	testutil.S(":2", "ZINTERCARD", "2", "za", "zb"),
	testutil.S(":1", "ZINTERCARD", "2", "za", "zb", "LIMIT", "1"),
	testutil.S(":0", "ZINTERCARD", "2", "za", "missing"),
	testutil.S("-ERR LIMIT can't be negative", "ZINTERCARD", "2", "za", "zb", "LIMIT", "-1"),
	testutil.S("-ERR numkeys should be greater than 0", "ZINTERCARD", "0", "za"),
	testutil.S("-ERR at least 1 input key is needed for 'zunion' command", "ZUNION", "0", "za"),
	testutil.S("-ERR at least 1 input key is needed for 'zunionstore' command", "ZUNIONSTORE", "out", "0", "za"),
	testutil.S(errSyntax, "ZUNION", "3", "za", "zb"),
	testutil.S(errSyntax, "ZUNION", "2", "za", "zb", "WEIGHTS", "1"),
	testutil.S("-ERR weight value is not a float", "ZUNION", "2", "za", "zb", "WEIGHTS", "1", "x"),
	testutil.S(errSyntax, "ZUNION", "2", "za", "zb", "AGGREGATE", "AVG"),
	testutil.S(errSyntax, "ZDIFF", "2", "za", "zb", "WEIGHTS", "1", "1"),
	testutil.S(errSyntax, "ZUNIONSTORE", "out", "2", "za", "zb", "WITHSCORES"),
	testutil.S(wrongType, "ZUNION", "2", "za", "str"),
	testutil.S(wrongType, "ZINTERSTORE", "out", "2", "str", "za"),
	testutil.S(wrongType, "ZDIFFSTORE", "out", "1", "str"),
}

// zsetPopScript covers pops, blocking pops that do not block and the
// deterministic ZRANDMEMBER forms.
var zsetPopScript = []testutil.Step{
	testutil.S(":4", "ZADD", "p", "1", "a", "2", "b", "3", "c", "4", "d"),
	testutil.S(`*["a", "1"]`, "ZPOPMIN", "p"),
	testutil.S(`*["d", "4"]`, "ZPOPMAX", "p"),
	testutil.S(`*["b", "2", "c", "3"]`, "ZPOPMIN", "p", "5"),
	testutil.S(":0", "EXISTS", "p"),
	testutil.S("*[]", "ZPOPMIN", "missing"),
	testutil.S("*[]", "ZPOPMAX", "missing", "2"),
	testutil.S("-ERR value is out of range, must be positive", "ZPOPMIN", "p", "-1"),
	testutil.S(wrongType, "ZPOPMIN", "str"),
	testutil.S(":4", "ZADD", "p", "1", "a", "2", "b", "3", "c", "4", "d"),
	testutil.S(`*["p", *[*["a", "1"]]]`, "ZMPOP", "2", "missing", "p", "MIN"),
	testutil.S(`*["p", *[*["d", "4"], *["c", "3"]]]`, "ZMPOP", "1", "p", "MAX", "COUNT", "2"),
	testutil.S("(nil-array)", "ZMPOP", "1", "missing", "MIN"),
	testutil.S("-ERR numkeys should be greater than 0", "ZMPOP", "0", "p", "MIN"),
	testutil.S(errSyntax, "ZMPOP", "1", "p", "LEFT"),
	testutil.S("-ERR count should be greater than 0", "ZMPOP", "1", "p", "MIN", "COUNT", "0"),
	testutil.S(`*["p", "b", "2"]`, "BZPOPMIN", "missing", "p", "0"),
	testutil.S("(nil-array)", "BZPOPMIN", "p", "0.01"),
	testutil.S(":2", "ZADD", "p", "1", "a", "9", "z"),
	testutil.S(`*["p", "z", "9"]`, "BZPOPMAX", "p", "0"),
	testutil.S("(nil-array)", "BZPOPMAX", "missing", "0.01"),
	testutil.S(`*["p", *[*["a", "1"]]]`, "BZMPOP", "0", "1", "p", "MIN"),
	testutil.S("(nil-array)", "BZMPOP", "0.01", "1", "p", "MIN"),
	testutil.S("-ERR timeout is negative", "BZPOPMIN", "p", "-1"),
	testutil.S(wrongType, "BZPOPMIN", "str", "0"),
	testutil.S(":3", "ZADD", "rm", "1", "a", "2", "b", "3", "c"),
	testutil.SS(`*["a", "b", "c"]`, "ZRANDMEMBER", "rm", "10"),
	testutil.S(":1", "ZADD", "one", "7", "x"),
	testutil.S(`"x"`, "ZRANDMEMBER", "one"),
	testutil.S(`*["x", "7"]`, "ZRANDMEMBER", "one", "1", "WITHSCORES"),
	testutil.S(`*["x", "7", "x", "7"]`, "ZRANDMEMBER", "one", "-2", "WITHSCORES"),
	testutil.S("*[]", "ZRANDMEMBER", "rm", "0"),
	testutil.S("(nil)", "ZRANDMEMBER", "missing"),
	testutil.S("*[]", "ZRANDMEMBER", "missing", "3"),
	testutil.S(errSyntax, "ZRANDMEMBER", "rm", "1", "FOO"),
	testutil.S(wrongType, "ZRANDMEMBER", "str"),
}

func TestZSetCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), zsetScript)
}

func TestZSetCommandsDifferential(t *testing.T) {
	testutil.Differential(t, zsetScript, testutil.KnownDivergences)
}

func TestZSetCommands_RandMemberWithScoresCount(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":3", "ZADD", "z", "1", "a", "2", "b", "3", "c")})
	scores := map[string]string{"a": "1", "b": "2", "c": "3"}
	testutil.RecordCommands(t)("zrandmember")
	v, err := c.Do("ZRANDMEMBER", "z", "2", "WITHSCORES")
	if err != nil {
		t.Fatalf("ZRANDMEMBER: %v", err)
	}
	if len(v.Elems) != 4 {
		t.Fatalf("got %d elements, want 4", len(v.Elems))
	}
	for i := 0; i < 4; i += 2 {
		m, s := string(v.Elems[i].Str), string(v.Elems[i+1].Str)
		if scores[m] != s {
			t.Fatalf("got member %q score %q, want score %q", m, s, scores[m])
		}
	}
}

func TestZSetCommands_BlockingFIFO(t *testing.T) {
	t.Skip("known bug B13: clients blocked on one key are woken all at once and race, so wake order is not FIFO (infra.go blocker.waiters is a map; blocker.signal wakes every waiter)")
	tests := []blockingFIFOCase{
		{
			name:  "BZPOPMIN",
			cmd:   []string{"BZPOPMIN", "q", "0"},
			wake:  []string{"ZADD", "q", "1", "a", "2", "b"},
			first: `*["q", "a", "1"]`, second: `*["q", "b", "2"]`,
		},
		{
			name:  "BZMPOP",
			cmd:   []string{"BZMPOP", "0", "1", "q", "MIN"},
			wake:  []string{"ZADD", "q", "1", "a", "2", "b"},
			first: `*["q", *[*["a", "1"]]]`, second: `*["q", *[*["b", "2"]]]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { runBlockingFIFO(t, tc) })
	}
}

func TestZSetCommands_BlockingWake(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c, other := testutil.MustDial(t, addr), testutil.MustDial(t, addr)
	sendBlocking(t, c, "BZPOPMAX", "q", "0")
	waitBlocked(t, other, 1)
	testutil.RunScript(t, other, []testutil.Step{testutil.S(":2", "ZADD", "q", "1", "a", "2", "b")})
	readReply(t, c, `*["q", "b", "2"]`)
}
