// Integration and differential tests for the set command family. Expected
// replies are Redis 8.4's; the same script runs against real Redis in CI
// through testutil.Differential. Random replies (SPOP, SRANDMEMBER) are only
// asserted where they are deterministic.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// setScript covers every set command with deterministic steps.
var setScript = concat(setBasicScript, setAlgebraScript, setRandomScript)

var setBasicScript = []testutil.Step{
	testutil.S(":3", "SADD", "s", "a", "b", "c"),
	testutil.S(":1", "SADD", "s", "c", "d"),
	testutil.S(":0", "SADD", "s", "a"),
	testutil.S(":4", "SCARD", "s"),
	testutil.S(":0", "SCARD", "missing"),
	testutil.SS(`*["a", "b", "c", "d"]`, "SMEMBERS", "s"),
	testutil.S("*[]", "SMEMBERS", "missing"),
	testutil.S(":1", "SISMEMBER", "s", "a"),
	testutil.S(":0", "SISMEMBER", "s", "z"),
	testutil.S(":0", "SISMEMBER", "missing", "a"),
	testutil.S("*[:1, :0, :1]", "SMISMEMBER", "s", "a", "z", "d"),
	testutil.S("*[:0]", "SMISMEMBER", "missing", "a"),
	testutil.S(":2", "SREM", "s", "a", "b", "z"),
	testutil.S(":0", "SREM", "missing", "a"),
	testutil.S(":1", "SMOVE", "s", "s2", "c"),
	testutil.S(":0", "SMOVE", "s", "s2", "c"),
	testutil.S(":0", "SMOVE", "missing", "s2", "c"),
	testutil.S(":1", "SADD", "s", "c"),
	testutil.S(":1", "SMOVE", "s", "s2", "c"),
	testutil.SS(`*["c"]`, "SMEMBERS", "s2"),
	testutil.S(":1", "SMOVE", "s", "s", "d"),
	testutil.S(":1", "SMOVE", "s", "s2", "d"),
	testutil.S(":0", "EXISTS", "s"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "SADD", "str", "a"),
	testutil.S(wrongType, "SMEMBERS", "str"),
	testutil.S(wrongType, "SISMEMBER", "str", "a"),
	testutil.S(wrongType, "SMOVE", "str", "s2", "a"),
	testutil.S(wrongType, "SMOVE", "s2", "str", "c"),
	testutil.S(wrongType, "SCARD", "str"),
	testutil.S(arityErr("sadd"), "SADD", "s"),
	testutil.S(arityErr("srem"), "SREM", "s"),
	testutil.S(arityErr("smismember"), "SMISMEMBER", "s"),
	testutil.S(arityErr("smove"), "SMOVE", "s", "s2"),
	testutil.S(":5", "SADD", "sc", "m1", "m2", "m3", "m4", "m5"),
	testutil.S(`*["0", *["m1"]]`, "SSCAN", "sc", "0", "MATCH", "m1", "COUNT", "100"),
	testutil.S(`*["0", *[]]`, "SSCAN", "missing", "0"),
	testutil.S("-ERR invalid cursor", "SSCAN", "sc", "x"),
	testutil.S(errSyntax, "SSCAN", "sc", "0", "FOO"),
	testutil.S(wrongType, "SSCAN", "str", "0"),
}

var setAlgebraScript = []testutil.Step{
	testutil.S(":4", "SADD", "x", "a", "b", "c", "d"),
	testutil.S(":3", "SADD", "y", "c", "d", "e"),
	testutil.S(":2", "SADD", "z", "d", "f"),
	testutil.SS(`*["c", "d"]`, "SINTER", "x", "y"),
	testutil.SS(`*["d"]`, "SINTER", "x", "y", "z"),
	testutil.S("*[]", "SINTER", "x", "missing"),
	testutil.SS(`*["a", "b", "c", "d", "e", "f"]`, "SUNION", "x", "y", "z", "missing"),
	testutil.SS(`*["a", "b"]`, "SDIFF", "x", "y", "z"),
	testutil.SS(`*["a", "b", "c", "d"]`, "SDIFF", "x", "missing"),
	testutil.S("*[]", "SDIFF", "missing", "x"),
	testutil.S(":2", "SINTERSTORE", "d1", "x", "y"),
	testutil.SS(`*["c", "d"]`, "SMEMBERS", "d1"),
	testutil.S(":0", "SINTERSTORE", "d1", "x", "missing"),
	testutil.S(":0", "EXISTS", "d1"),
	testutil.S(":6", "SUNIONSTORE", "d2", "x", "y", "z"),
	testutil.S(":2", "SDIFFSTORE", "d3", "x", "y", "z"),
	testutil.SS(`*["a", "b"]`, "SMEMBERS", "d3"),
	testutil.S(":4", "SDIFFSTORE", "x", "x", "missing"),
	testutil.S(":2", "SINTERCARD", "2", "x", "y"),
	testutil.S(":1", "SINTERCARD", "3", "x", "y", "z"),
	testutil.S(":1", "SINTERCARD", "2", "x", "y", "LIMIT", "1"),
	testutil.S(":2", "SINTERCARD", "2", "x", "y", "LIMIT", "0"),
	testutil.S(":0", "SINTERCARD", "2", "x", "missing"),
	testutil.S("-ERR numkeys should be greater than 0", "SINTERCARD", "0", "x"),
	testutil.S("-ERR Number of keys can't be greater than number of args", "SINTERCARD", "3", "x", "y"),
	testutil.S("-ERR LIMIT can't be negative", "SINTERCARD", "2", "x", "y", "LIMIT", "-1"),
	testutil.S(errSyntax, "SINTERCARD", "2", "x", "y", "FOO"),
	testutil.S(wrongType, "SINTER", "x", "str"),
	testutil.S(wrongType, "SUNION", "str", "x"),
	testutil.S(wrongType, "SDIFF", "x", "str"),
	testutil.S(wrongType, "SINTERSTORE", "d1", "x", "str"),
	testutil.S(wrongType, "SUNIONSTORE", "d1", "str"),
	testutil.S(wrongType, "SDIFFSTORE", "d1", "str"),
	testutil.S(wrongType, "SINTERCARD", "1", "str"),
	testutil.S("+OK", "SET", "dstr", "v"),
	testutil.S(":2", "SUNIONSTORE", "dstr", "z", "missing", "z"),
	testutil.SS(`*["d", "f"]`, "SMEMBERS", "dstr"),
	testutil.S(arityErr("sinter"), "SINTER"),
	testutil.S(arityErr("sinterstore"), "SINTERSTORE", "d1"),
}

// setRandomScript only has steps whose replies do not depend on randomness.
var setRandomScript = []testutil.Step{
	testutil.S(":3", "SADD", "r", "a", "b", "c"),
	testutil.SS(`*["a", "b", "c"]`, "SRANDMEMBER", "r", "10"),
	testutil.SS(`*["a", "b", "c"]`, "SRANDMEMBER", "r", "3"),
	testutil.S("*[]", "SRANDMEMBER", "r", "0"),
	testutil.S("(nil)", "SRANDMEMBER", "missing"),
	testutil.S("*[]", "SRANDMEMBER", "missing", "5"),
	testutil.S(":1", "SADD", "one", "x"),
	testutil.S(`"x"`, "SRANDMEMBER", "one"),
	testutil.S(`*["x", "x", "x"]`, "SRANDMEMBER", "one", "-3"),
	testutil.S(errNotInt, "SRANDMEMBER", "r", "x"),
	testutil.S(wrongType, "SRANDMEMBER", "str"),
	testutil.S(`"x"`, "SPOP", "one"),
	testutil.S(":0", "EXISTS", "one"),
	testutil.S("(nil)", "SPOP", "missing"),
	testutil.S("*[]", "SPOP", "missing", "2"),
	testutil.S("*[]", "SPOP", "r", "0"),
	testutil.SS(`*["a", "b", "c"]`, "SPOP", "r", "5"),
	testutil.S(":0", "EXISTS", "r"),
	testutil.S("-ERR value is out of range, must be positive", "SPOP", "r", "-1"),
	testutil.S(wrongType, "SPOP", "str"),
	testutil.S(errSyntax, "SPOP", "r", "1", "2"),
}

func TestSetCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), setScript)
}

func TestSetCommandsDifferential(t *testing.T) {
	testutil.Differential(t, setScript, testutil.KnownDivergences)
}

// TestSetCommands_RandomCounts asserts reply lengths and membership for the
// random-reply commands.
func TestSetCommands_RandomCounts(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":5", "SADD", "s", "a", "b", "c", "d", "e")})
	members := map[string]bool{`"a"`: true, `"b"`: true, `"c"`: true, `"d"`: true, `"e"`: true}
	tests := []struct {
		name    string
		cmd     []string
		wantLen int
		unique  bool
	}{
		{"SRANDMEMBER positive count is distinct", []string{"SRANDMEMBER", "s", "3"}, 3, true},
		{"SRANDMEMBER negative count may repeat", []string{"SRANDMEMBER", "s", "-12"}, 12, false},
		{"SPOP count", []string{"SPOP", "s", "2"}, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testutil.RecordCommands(t)(tc.cmd[0])
			v, err := c.Do(tc.cmd...)
			if err != nil {
				t.Fatalf("%v: %v", tc.cmd, err)
			}
			if len(v.Elems) != tc.wantLen {
				t.Fatalf("%v: got %d elements, want %d", tc.cmd, len(v.Elems), tc.wantLen)
			}
			seen := map[string]bool{}
			for _, e := range v.Elems {
				r := testutil.Render(e, false)
				if !members[r] {
					t.Fatalf("%v: got non-member %s", tc.cmd, r)
				}
				if tc.unique && seen[r] {
					t.Fatalf("%v: got duplicate %s", tc.cmd, r)
				}
				seen[r] = true
			}
		})
	}
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":3", "SCARD", "s")})
}
