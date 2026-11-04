// Integration and differential tests for the hash command family, including
// hash-field expiration (HEXPIRE family, HGETDEL/HGETEX/HSETEX). Expected
// replies are Redis 8.4's; the time-independent script also runs against real
// Redis in CI through testutil.Differential.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// hashScript covers every hash command with time-independent steps.
var hashScript = concat(hashBasicScript, hashNumericScript, hashFieldTTLScript, hashGetSetExScript)

var hashBasicScript = []testutil.Step{
	testutil.S(":2", "HSET", "h", "a", "1", "b", "2"),
	testutil.S(":1", "HSET", "h", "a", "10", "c", "3"),
	testutil.S("+OK", "HMSET", "h", "d", "4"),
	testutil.S(":0", "HSETNX", "h", "a", "x"),
	testutil.S(":1", "HSETNX", "h", "e", "hello"),
	testutil.S(`"10"`, "HGET", "h", "a"),
	testutil.S("(nil)", "HGET", "h", "nope"),
	testutil.S("(nil)", "HGET", "missing", "a"),
	testutil.S(`*["10", (nil), "4"]`, "HMGET", "h", "a", "nope", "d"),
	testutil.S(`*[(nil)]`, "HMGET", "missing", "a"),
	testutil.S(":5", "HLEN", "h"),
	testutil.S(":0", "HLEN", "missing"),
	testutil.S(":5", "HSTRLEN", "h", "e"),
	testutil.S(":0", "HSTRLEN", "h", "nope"),
	testutil.S(":1", "HEXISTS", "h", "a"),
	testutil.S(":0", "HEXISTS", "h", "nope"),
	testutil.SS(`*["a", "b", "c", "d", "e"]`, "HKEYS", "h"),
	testutil.SS(`*["10", "2", "3", "4", "hello"]`, "HVALS", "h"),
	testutil.S(":2", "HDEL", "h", "d", "e", "nope"),
	testutil.S(":0", "HDEL", "missing", "a"),
	testutil.S(":1", "HSET", "small", "f", "v"),
	testutil.S(`*["f", "v"]`, "HGETALL", "small"),
	testutil.S("*[]", "HGETALL", "missing"),
	testutil.S("*[]", "HKEYS", "missing"),
	testutil.S(`*["0", *["f", "v"]]`, "HSCAN", "small", "0"),
	testutil.S(`*["0", *["f"]]`, "HSCAN", "small", "0", "NOVALUES"),
	testutil.S(`*["0", *[]]`, "HSCAN", "small", "0", "MATCH", "x*"),
	testutil.S(`*["0", *[]]`, "HSCAN", "missing", "0"),
	testutil.S("-ERR invalid cursor", "HSCAN", "small", "x"),
	testutil.S(`"f"`, "HRANDFIELD", "small"),
	testutil.S(`*["f", "v"]`, "HRANDFIELD", "small", "5", "WITHVALUES"),
	testutil.S(`*["f", "v", "f", "v"]`, "HRANDFIELD", "small", "-2", "WITHVALUES"),
	testutil.SS(`*["a", "b", "c"]`, "HRANDFIELD", "h", "3"),
	testutil.S("*[]", "HRANDFIELD", "h", "0"),
	testutil.S("(nil)", "HRANDFIELD", "missing"),
	testutil.S("*[]", "HRANDFIELD", "missing", "2"),
	testutil.S(errSyntax, "HRANDFIELD", "h", "1", "FOO"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "HSET", "str", "a", "1"),
	testutil.S(wrongType, "HGET", "str", "a"),
	testutil.S(wrongType, "HGETALL", "str"),
	testutil.S(wrongType, "HLEN", "str"),
	testutil.S(wrongType, "HSCAN", "str", "0"),
	testutil.S(wrongType, "HRANDFIELD", "str"),
	testutil.S(arityErr("hset"), "HSET", "h", "a"),
	testutil.S(arityErr("hset"), "HSET", "h", "a", "1", "b"),
	testutil.S(arityErr("hmset"), "HMSET", "h", "a"),
	testutil.S(arityErr("hdel"), "HDEL", "h"),
	testutil.S(arityErr("hmget"), "HMGET", "h"),
}

var hashNumericScript = []testutil.Step{
	testutil.S(":5", "HINCRBY", "n", "i", "5"),
	testutil.S(":-5", "HINCRBY", "n", "i", "-10"),
	testutil.S(errNotInt, "HINCRBY", "n", "i", "x"),
	testutil.S(":1", "HSET", "n", "s", "abc"),
	testutil.S("-ERR hash value is not an integer", "HINCRBY", "n", "s", "1"),
	testutil.S(":1", "HSET", "n", "big", "9223372036854775807"),
	testutil.S("-ERR increment or decrement would overflow", "HINCRBY", "n", "big", "1"),
	testutil.S(`"10.5"`, "HINCRBYFLOAT", "n", "f", "10.5"),
	testutil.S(`"10.6"`, "HINCRBYFLOAT", "n", "f", "0.1"),
	testutil.S(`"5010.6"`, "HINCRBYFLOAT", "n", "f", "5.0e3"),
	testutil.S(`"-4.5"`, "HINCRBYFLOAT", "n", "i", "0.5"),
	testutil.S("-ERR hash value is not a float", "HINCRBYFLOAT", "n", "s", "1"),
	testutil.S(errNotFloat, "HINCRBYFLOAT", "n", "f", "x"),
	testutil.S(errNotFloat, "HINCRBYFLOAT", "n", "f", "nan"),
	testutil.S(wrongType, "HINCRBY", "str", "i", "1"),
	testutil.S(wrongType, "HINCRBYFLOAT", "str", "f", "1"),
}

// hashFieldTTLScript uses absolute expiry times far in the future so the
// replies do not depend on the wall clock.
var hashFieldTTLScript = []testutil.Step{
	testutil.S(":3", "HSET", "x", "a", "1", "b", "2", "c", "3"),
	testutil.S("*[:1, :-2]", "HEXPIREAT", "x", farFuture, "FIELDS", "2", "a", "nope"),
	testutil.S("*[:"+farFuture+", :-1, :-2]", "HEXPIRETIME", "x", "FIELDS", "3", "a", "b", "nope"),
	testutil.S("*[:"+farFutureMS+"]", "HPEXPIRETIME", "x", "FIELDS", "1", "a"),
	testutil.S("*[:0]", "HEXPIREAT", "x", farFuture, "NX", "FIELDS", "1", "a"),
	testutil.S("*[:1]", "HEXPIREAT", "x", farFuture, "XX", "FIELDS", "1", "a"),
	testutil.S("*[:0]", "HEXPIREAT", "x", farFuture, "XX", "FIELDS", "1", "b"),
	testutil.S("*[:0]", "HEXPIREAT", "x", "4102444700", "GT", "FIELDS", "1", "a"),
	testutil.S("*[:1]", "HEXPIREAT", "x", "4102444900", "GT", "FIELDS", "1", "a"),
	testutil.S("*[:1]", "HEXPIREAT", "x", "4102444700", "LT", "FIELDS", "1", "a"),
	testutil.S("*[:1]", "HEXPIREAT", "x", farFuture, "LT", "FIELDS", "1", "b"),
	testutil.S("*[:0]", "HEXPIREAT", "x", farFuture, "GT", "FIELDS", "1", "c"),
	testutil.S("*[:1]", "HPEXPIREAT", "x", farFutureMS, "FIELDS", "1", "c"),
	testutil.S("*[:"+farFuture+"]", "HEXPIRETIME", "x", "FIELDS", "1", "c"),
	testutil.S("*[:1, :-2]", "HPERSIST", "x", "FIELDS", "2", "c", "nope"),
	testutil.S("*[:-1]", "HPERSIST", "x", "FIELDS", "1", "c"),
	testutil.S("*[:-1]", "HTTL", "x", "FIELDS", "1", "c"),
	testutil.S("*[:-1]", "HPTTL", "x", "FIELDS", "1", "c"),
	testutil.S("*[:-2]", "HTTL", "x", "FIELDS", "1", "nope"),
	testutil.S("*[:2]", "HEXPIRE", "x", "0", "FIELDS", "1", "c"),
	testutil.S(":0", "HEXISTS", "x", "c"),
	testutil.S("*[:2]", "HPEXPIRE", "x", "0", "FIELDS", "1", "b"),
	testutil.S("*[:2]", "HEXPIREAT", "x", "1", "FIELDS", "1", "a"),
	testutil.S(":0", "EXISTS", "x"),
	testutil.S("*[:-2, :-2]", "HEXPIRE", "missing", "10", "FIELDS", "2", "a", "b"),
	testutil.S("*[:-2]", "HTTL", "missing", "FIELDS", "1", "a"),
	testutil.S("*[:-2]", "HPERSIST", "missing", "FIELDS", "1", "a"),
	testutil.S(":1", "HSET", "x", "a", "1"),
	testutil.S("-ERR Parameter `numFields` should be greater than 0", "HEXPIRE", "x", "10", "FIELDS", "0", "a"),
	testutil.S("-ERR The `numfields` parameter must match the number of arguments", "HEXPIRE", "x", "10", "FIELDS", "2", "a"),
	testutil.S("-ERR Mandatory argument FIELDS is missing or not at the right position", "HEXPIRE", "x", "10", "a", "b", "c"),
	testutil.S(errNotInt, "HEXPIRE", "x", "abc", "FIELDS", "1", "a"),
	testutil.S(wrongType, "HEXPIRE", "str", "10", "FIELDS", "1", "a"),
	testutil.S(wrongType, "HTTL", "str", "FIELDS", "1", "a"),
}

var hashGetSetExScript = []testutil.Step{
	testutil.S(":3", "HSET", "g", "a", "1", "b", "2", "c", "3"),
	testutil.S(`*["1", (nil)]`, "HGETDEL", "g", "FIELDS", "2", "a", "nope"),
	testutil.S(":0", "HEXISTS", "g", "a"),
	testutil.S(`*[(nil)]`, "HGETDEL", "missing", "FIELDS", "1", "a"),
	testutil.S(`*["2", "3"]`, "HGETDEL", "g", "FIELDS", "2", "b", "c"),
	testutil.S(":0", "EXISTS", "g"),
	testutil.S(":2", "HSET", "g", "a", "1", "b", "2"),
	testutil.S(`*["1", "2"]`, "HGETEX", "g", "FIELDS", "2", "a", "b"),
	testutil.S(`*["1", (nil)]`, "HGETEX", "g", "EXAT", farFuture, "FIELDS", "2", "a", "nope"),
	testutil.S("*[:"+farFuture+"]", "HEXPIRETIME", "g", "FIELDS", "1", "a"),
	testutil.S(`*["1"]`, "HGETEX", "g", "PXAT", farFutureMS, "FIELDS", "1", "a"),
	testutil.S(`*["1"]`, "HGETEX", "g", "PERSIST", "FIELDS", "1", "a"),
	testutil.S("*[:-1]", "HEXPIRETIME", "g", "FIELDS", "1", "a"),
	testutil.S(`*["2"]`, "HGETEX", "g", "EX", "100", "FIELDS", "1", "b"),
	testutil.S(`*["2"]`, "HGETEX", "g", "PX", "100000", "FIELDS", "1", "b"),
	testutil.S(`*[(nil)]`, "HGETEX", "missing", "EX", "10", "FIELDS", "1", "a"),
	testutil.S(":1", "HSETEX", "s", "FIELDS", "1", "a", "1"),
	testutil.S(":0", "HSETEX", "s", "FNX", "FIELDS", "2", "a", "x", "b", "y"),
	testutil.S(":1", "HSETEX", "s", "FNX", "FIELDS", "1", "b", "2"),
	testutil.S(":0", "HSETEX", "s", "FXX", "FIELDS", "2", "a", "x", "c", "y"),
	testutil.S(":1", "HSETEX", "s", "FXX", "EXAT", farFuture, "FIELDS", "2", "a", "10", "b", "20"),
	testutil.S("*[:"+farFuture+", :"+farFuture+"]", "HEXPIRETIME", "s", "FIELDS", "2", "a", "b"),
	testutil.S(":1", "HSETEX", "s", "KEEPTTL", "FIELDS", "1", "a", "11"),
	testutil.S("*[:"+farFuture+"]", "HEXPIRETIME", "s", "FIELDS", "1", "a"),
	testutil.S(":1", "HSETEX", "s", "FIELDS", "1", "a", "12"),
	testutil.S("*[:-1]", "HEXPIRETIME", "s", "FIELDS", "1", "a"),
	testutil.S(":1", "HSETEX", "s", "PXAT", farFutureMS, "FIELDS", "1", "c", "3"),
	testutil.S(`*["12", "20", "3"]`, "HMGET", "s", "a", "b", "c"),
	testutil.S(":0", "HSETEX", "nk", "FXX", "FIELDS", "1", "a", "1"),
	testutil.S(":0", "EXISTS", "nk"),
	testutil.S(wrongType, "HGETDEL", "str", "FIELDS", "1", "a"),
	testutil.S(wrongType, "HGETEX", "str", "FIELDS", "1", "a"),
	testutil.S(wrongType, "HSETEX", "str", "FIELDS", "1", "a", "1"),
}

func TestHashCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), hashScript)
}

func TestHashCommandsDifferential(t *testing.T) {
	testutil.Differential(t, hashScript, testutil.KnownDivergences)
}

// TestHashCommands_FieldTTLWithFakeClock checks relative field TTLs and lazy
// field expiry on a FakeClock server, so the replies are exact.
func TestHashCommands_FieldTTLWithFakeClock(t *testing.T) {
	fc, c := startWithClock(t, nil)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(":3", "HSET", "h", "f", "v", "g", "w", "p", "x"),
		testutil.S("*[:1]", "HEXPIRE", "h", "1", "FIELDS", "1", "f"),
		testutil.S("*[:1]", "HTTL", "h", "FIELDS", "1", "f"),
		testutil.S("*[:1000]", "HPTTL", "h", "FIELDS", "1", "f"),
		testutil.S("*[:1700000001]", "HEXPIRETIME", "h", "FIELDS", "1", "f"),
		testutil.S("*[:1]", "HPEXPIRE", "h", "2500", "FIELDS", "1", "g"),
		testutil.S("*[:2500]", "HPTTL", "h", "FIELDS", "1", "g"),
		testutil.S("*[:3]", "HTTL", "h", "FIELDS", "1", "g"),
		testutil.S("*[:1]", "HEXPIRE", "h", "100", "FIELDS", "1", "p"),
		testutil.S("*[:1]", "HPERSIST", "h", "FIELDS", "1", "p"),
		testutil.S("*[:-1]", "HTTL", "h", "FIELDS", "1", "p"),
		testutil.S(`*["w"]`, "HGETEX", "h", "EX", "30", "FIELDS", "1", "g"),
		testutil.S("*[:30]", "HTTL", "h", "FIELDS", "1", "g"),
		testutil.S(":1", "HSETEX", "h", "PX", "1500", "FIELDS", "1", "q", "y"),
		testutil.S("*[:1500]", "HPTTL", "h", "FIELDS", "1", "q"),
	})
	fc.Advance(1600 * time.Millisecond)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("(nil)", "HGET", "h", "f"),
		testutil.S("(nil)", "HGET", "h", "q"),
		testutil.S("*[:-2]", "HTTL", "h", "FIELDS", "1", "f"),
		testutil.S("*[:-2]", "HPERSIST", "h", "FIELDS", "1", "f"),
		testutil.S("*[:-2]", "HEXPIRE", "h", "10", "FIELDS", "1", "f"),
		testutil.S(":2", "HLEN", "h"),
		testutil.SS(`*["g", "p"]`, "HKEYS", "h"),
		testutil.S("*[:28400]", "HPTTL", "h", "FIELDS", "1", "g"),
	})
	fc.Advance(30 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`*["p", "x"]`, "HGETALL", "h"),
	})
}
