// Regression tests for defects found in the 2026-11-23 code review of the
// string, key and set command families. Expected replies are Redis 8.4's.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// reviewFixScript is time-independent, so it also runs against a real Redis.
var reviewFixScript = []testutil.Step{
	// SET replaces a key of any type; only the GET option checks the type.
	testutil.S(":1", "RPUSH", "rv:l", "a"),
	testutil.S("+OK", "SET", "rv:l", "v"),
	testutil.S(`"v"`, "GET", "rv:l"),
	testutil.S(":1", "HSET", "rv:h", "f", "1"),
	testutil.S("+OK", "SET", "rv:h", "v", "XX"),
	testutil.S("+string", "TYPE", "rv:h"),
	testutil.S(":1", "SADD", "rv:s", "m"),
	testutil.S("(nil)", "SET", "rv:s", "v", "NX"),
	testutil.S("+set", "TYPE", "rv:s"),
	testutil.S("+OK", "SET", "rv:s", "v", "EX", "100"),
	testutil.S("+string", "TYPE", "rv:s"),
	testutil.S(":1", "ZADD", "rv:z", "1", "m"),
	testutil.S(wrongType, "SET", "rv:z", "v", "GET"),
	testutil.S("+zset", "TYPE", "rv:z"),

	// Offsets and counts near the int64 limit must not overflow.
	testutil.S("+OK", "SET", "rv:str", "foobar"),
	testutil.S("-ERR string exceeds maximum allowed size (proto-max-bulk-len)", "SETRANGE", "rv:str", "9223372036854775807", "x"),
	testutil.S("-ERR string exceeds maximum allowed size (proto-max-bulk-len)", "SETRANGE", "rv:str", "536870912", "x"),
	testutil.S(`"foobar"`, "GET", "rv:str"),
	testutil.S(":3", "RPUSH", "rv:sort", "3", "1", "2"),
	testutil.S(`*["2", "3"]`, "SORT", "rv:sort", "LIMIT", "1", "9223372036854775807"),
	testutil.S("*[]", "SORT", "rv:sort", "LIMIT", "9223372036854775807", "9223372036854775807"),
	testutil.S(`*["1", "2", "3"]`, "SORT", "rv:sort", "LIMIT", "0", "-1"),

	// A repeated LIMIT is accepted and the last one wins.
	testutil.S(":3", "SADD", "rv:set", "a", "b", "c"),
	testutil.S(":2", "SINTERCARD", "1", "rv:set", "LIMIT", "1", "LIMIT", "2"),
	testutil.S(":3", "ZADD", "rv:zs", "1", "a", "2", "b", "3", "c"),
	testutil.S(":2", "ZINTERCARD", "1", "rv:zs", "LIMIT", "1", "LIMIT", "2"),
	testutil.S(errSyntax, "SINTERCARD", "1", "rv:set", "LIMIT"),
	testutil.S(errSyntax, "ZINTERCARD", "1", "rv:zs", "LIMIT", "1", "FOO"),
	testutil.S("-ERR LIMIT can't be negative", "SINTERCARD", "1", "rv:set", "LIMIT", "-1"),
}

func TestReviewFixes(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), reviewFixScript)
}

func TestReviewFixesDifferential(t *testing.T) {
	testutil.Differential(t, reviewFixScript, testutil.KnownDivergences)
}
