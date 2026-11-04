// Integration and differential tests for the string command family. The
// expected replies are Redis 8.4's; the same script runs against real Redis
// in CI through testutil.Differential.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// Reply strings shared by the command-family scripts in this package.
const (
	wrongType = "-WRONGTYPE Operation against a key holding the wrong kind of value"
	errNotInt = "-ERR value is not an integer or out of range"
	errSyntax = "-ERR syntax error"
	// farFuture is 2100-01-01T00:00:00Z: an absolute expiry that makes
	// EXPIRETIME replies identical on DistCache and Redis.
	farFuture   = "4102444800"
	farFutureMS = "4102444800000"
)

// arityErr is Redis's wrong-number-of-arguments error for cmd.
func arityErr(cmd string) string {
	return "-ERR wrong number of arguments for '" + cmd + "' command"
}

// stringScript covers every string command with time-independent steps.
var stringScript = concat(stringSetScript, stringGetScript, stringNumericScript, stringLCSScript)

var stringSetScript = []testutil.Step{
	testutil.S("+OK", "SET", "k", "v"),
	testutil.S(`"v"`, "GET", "k"),
	testutil.S("(nil)", "GET", "missing"),
	testutil.S(arityErr("get"), "GET"),
	testutil.S(arityErr("set"), "SET", "k"),
	testutil.S("(nil)", "SET", "k", "v1", "NX"),
	testutil.S("+OK", "SET", "k", "v2", "XX"),
	testutil.S("(nil)", "SET", "nk", "v", "XX"),
	testutil.S(`"v2"`, "SET", "k", "v3", "GET"),
	testutil.S("(nil)", "SET", "nk2", "v", "GET"),
	testutil.S(`"v3"`, "SET", "k", "v3", "NX", "GET"),
	testutil.S(errSyntax, "SET", "k", "v", "NX", "XX"),
	testutil.S(errSyntax, "SET", "k", "v", "EX", "10", "PX", "10"),
	testutil.S(errSyntax, "SET", "k", "v", "KEEPTTL", "EX", "10"),
	testutil.S(errSyntax, "SET", "k", "v", "EX"),
	testutil.S(errSyntax, "SET", "k", "v", "FOO"),
	testutil.S("-ERR invalid expire time in 'set' command", "SET", "k", "v", "EX", "0"),
	testutil.S("-ERR invalid expire time in 'set' command", "SET", "k", "v", "PX", "-1"),
	testutil.S(errNotInt, "SET", "k", "v", "EX", "abc"),
	testutil.S("+OK", "SET", "k", "v", "EXAT", farFuture),
	testutil.S(":"+farFuture, "EXPIRETIME", "k"),
	testutil.S("+OK", "SET", "k", "v", "PXAT", farFutureMS),
	testutil.S(":"+farFutureMS, "PEXPIRETIME", "k"),
	testutil.S("+OK", "SET", "k", "v2", "KEEPTTL"),
	testutil.S(":"+farFuture, "EXPIRETIME", "k"),
	testutil.S("+OK", "SET", "k", "v3"),
	testutil.S(":-1", "EXPIRETIME", "k"),
	testutil.S("+OK", "SET", "k", "v3", "EX", "100"),
	testutil.S("+OK", "SET", "k", "v3", "PX", "100000"),
	testutil.S(":1", "LPUSH", "lst", "a"),
	testutil.S(wrongType, "SET", "lst", "v", "GET"),
	testutil.S(wrongType, "GET", "lst"),
	testutil.S(":0", "SETNX", "k", "x"),
	testutil.S(":1", "SETNX", "n1", "x"),
	testutil.S(arityErr("setnx"), "SETNX", "k"),
	testutil.S("+OK", "SETEX", "se", "100", "v"),
	testutil.S(`"v"`, "GET", "se"),
	testutil.S("-ERR invalid expire time in 'setex' command", "SETEX", "se", "0", "v"),
	testutil.S(errNotInt, "SETEX", "se", "abc", "v"),
	testutil.S("+OK", "PSETEX", "pse", "100000", "v"),
	testutil.S("-ERR invalid expire time in 'psetex' command", "PSETEX", "pse", "-5", "v"),
	testutil.S(arityErr("psetex"), "PSETEX", "pse", "5"),
}

var stringGetScript = []testutil.Step{
	testutil.S(`"v3"`, "GETSET", "k", "new"),
	testutil.S(":-1", "TTL", "k"),
	testutil.S("(nil)", "GETSET", "gs", "x"),
	testutil.S(wrongType, "GETSET", "lst", "x"),
	testutil.S(`"new"`, "GETDEL", "k"),
	testutil.S("(nil)", "GETDEL", "k"),
	testutil.S(wrongType, "GETDEL", "lst"),
	testutil.S("+OK", "SET", "ge", "v"),
	testutil.S(`"v"`, "GETEX", "ge"),
	testutil.S(`"v"`, "GETEX", "ge", "EXAT", farFuture),
	testutil.S(":"+farFuture, "EXPIRETIME", "ge"),
	testutil.S(`"v"`, "GETEX", "ge", "PXAT", farFutureMS),
	testutil.S(`"v"`, "GETEX", "ge", "PERSIST"),
	testutil.S(":-1", "EXPIRETIME", "ge"),
	testutil.S(`"v"`, "GETEX", "ge", "EX", "100"),
	testutil.S(`"v"`, "GETEX", "ge", "PX", "100000"),
	testutil.S("-ERR invalid expire time in 'getex' command", "GETEX", "ge", "EX", "0"),
	testutil.S(errSyntax, "GETEX", "ge", "EX", "10", "PX", "10"),
	testutil.S(errSyntax, "GETEX", "ge", "FOO"),
	testutil.S("(nil)", "GETEX", "missing", "EX", "10"),
	testutil.S(wrongType, "GETEX", "lst"),
	testutil.S(arityErr("getex"), "GETEX"),
	testutil.S("+OK", "MSET", "a", "1", "b", "2"),
	testutil.S(arityErr("mset"), "MSET", "a"),
	testutil.S(arityErr("mset"), "MSET", "a", "1", "b"),
	testutil.S(`*["1", "2", (nil), (nil)]`, "MGET", "a", "b", "nope", "lst"),
	testutil.S(arityErr("mget"), "MGET"),
	testutil.S(":0", "MSETNX", "a", "1", "c", "3"),
	testutil.S(":0", "EXISTS", "c"),
	testutil.S(":1", "MSETNX", "c", "3", "d", "4"),
	testutil.S(arityErr("msetnx"), "MSETNX", "c"),
	testutil.S(":1", "STRLEN", "a"),
	testutil.S(":0", "STRLEN", "nope"),
	testutil.S(wrongType, "STRLEN", "lst"),
	testutil.S(":5", "APPEND", "ap", "hello"),
	testutil.S(":11", "APPEND", "ap", " world"),
	testutil.S(`"hello world"`, "GET", "ap"),
	testutil.S(wrongType, "APPEND", "lst", "x"),
	testutil.S(`"hello"`, "GETRANGE", "ap", "0", "4"),
	testutil.S(`"world"`, "GETRANGE", "ap", "-5", "-1"),
	testutil.S(`"hello world"`, "GETRANGE", "ap", "-100", "100"),
	testutil.S(`""`, "GETRANGE", "ap", "5", "1"),
	testutil.S(`""`, "GETRANGE", "nope", "0", "-1"),
	testutil.S(errNotInt, "GETRANGE", "ap", "0", "x"),
	testutil.S(wrongType, "GETRANGE", "lst", "0", "1"),
	testutil.S(`"hello"`, "SUBSTR", "ap", "0", "4"),
	testutil.S(arityErr("substr"), "SUBSTR", "ap", "0"),
	testutil.S(":11", "SETRANGE", "ap", "6", "Redis"),
	testutil.S(`"hello Redis"`, "GET", "ap"),
	testutil.S(":6", "SETRANGE", "sr", "5", "x"),
	testutil.S(`"\x00\x00\x00\x00\x00x"`, "GET", "sr"),
	testutil.S(":0", "SETRANGE", "empty", "0", ""),
	testutil.S(":0", "EXISTS", "empty"),
	testutil.S("-ERR offset is out of range", "SETRANGE", "sr", "-1", "x"),
	testutil.S("-ERR string exceeds maximum allowed size (proto-max-bulk-len)", "SETRANGE", "sr", "536870912", "x"),
	testutil.S(wrongType, "SETRANGE", "lst", "0", "x"),
}

var stringNumericScript = []testutil.Step{
	testutil.S(":1", "INCR", "i"),
	testutil.S(":11", "INCRBY", "i", "10"),
	testutil.S(":10", "DECR", "i"),
	testutil.S(":7", "DECRBY", "i", "3"),
	testutil.S(":-3", "INCRBY", "i", "-10"),
	testutil.S(":-1", "DECR", "neg"),
	testutil.S(errNotInt, "INCRBY", "i", "x"),
	testutil.S(errNotInt, "INCR", "ap"),
	testutil.S(wrongType, "INCR", "lst"),
	testutil.S(wrongType, "DECRBY", "lst", "1"),
	testutil.S(arityErr("incr"), "INCR"),
	testutil.S(arityErr("incrby"), "INCRBY", "i"),
	testutil.S(arityErr("decr"), "DECR"),
	testutil.S(arityErr("decrby"), "DECRBY", "i"),
	testutil.S("+OK", "SET", "big", "9223372036854775807"),
	testutil.S("-ERR increment or decrement would overflow", "INCR", "big"),
	testutil.S("-ERR increment or decrement would overflow", "INCRBY", "big", "1"),
	testutil.S("+OK", "SET", "small", "-9223372036854775808"),
	testutil.S("-ERR increment or decrement would overflow", "DECR", "small"),
	testutil.S("-ERR decrement would overflow", "DECRBY", "i", "-9223372036854775808"),
	testutil.S(errNotInt, "INCRBY", "i", "9223372036854775808"),
	testutil.S("+OK", "SET", "sp", " 1"),
	testutil.S(errNotInt, "INCR", "sp"),
	testutil.S(`"10.5"`, "INCRBYFLOAT", "f", "10.5"),
	testutil.S(`"10.6"`, "INCRBYFLOAT", "f", "0.1"),
	testutil.S(`"5010.6"`, "INCRBYFLOAT", "f", "5.0e3"),
	testutil.S(`"5000"`, "INCRBYFLOAT", "f", "-10.6"),
	testutil.S(`"4.5"`, "INCRBYFLOAT", "i3", "4.5"),
	testutil.S("-ERR value is not a valid float", "INCRBYFLOAT", "f", "nan"),
	testutil.S("-ERR value is not a valid float", "INCRBYFLOAT", "f", "abc"),
	testutil.S("-ERR value is not a valid float", "INCRBYFLOAT", "ap", "1"),
	testutil.S(wrongType, "INCRBYFLOAT", "lst", "1"),
	testutil.S(arityErr("incrbyfloat"), "INCRBYFLOAT", "f"),
	testutil.S(`"5000"`, "GET", "f"),
}

var stringLCSScript = []testutil.Step{
	testutil.S("+OK", "MSET", "k1", "ohmytext", "k2", "mynewtext"),
	testutil.S(`"mytext"`, "LCS", "k1", "k2"),
	testutil.S(":6", "LCS", "k1", "k2", "LEN"),
	testutil.S(`*["matches", *[*[*[:4, :7], *[:5, :8]], *[*[:2, :3], *[:0, :1]]], "len", :6]`, "LCS", "k1", "k2", "IDX"),
	testutil.S(`*["matches", *[*[*[:4, :7], *[:5, :8]]], "len", :6]`, "LCS", "k1", "k2", "IDX", "MINMATCHLEN", "4"),
	testutil.S(`*["matches", *[*[*[:4, :7], *[:5, :8], :4], *[*[:2, :3], *[:0, :1], :2]], "len", :6]`, "LCS", "k1", "k2", "IDX", "WITHMATCHLEN"),
	testutil.S("-ERR If you want both the length and indexes, please just use IDX.", "LCS", "k1", "k2", "LEN", "IDX"),
	testutil.S(`""`, "LCS", "k1", "nope"),
	testutil.S(":0", "LCS", "nope", "nope2", "LEN"),
	testutil.S(errSyntax, "LCS", "k1", "k2", "FOO"),
	testutil.S(errNotInt, "LCS", "k1", "k2", "IDX", "MINMATCHLEN", "x"),
	testutil.S(arityErr("lcs"), "LCS", "k1"),
}

// stringTTLScript runs on a FakeClock server, so TTL replies are exact.
var stringTTLScript = []testutil.Step{
	testutil.S("+OK", "SET", "t", "v", "EX", "100"),
	testutil.S(":100", "TTL", "t"),
	testutil.S(":100000", "PTTL", "t"),
	testutil.S("+OK", "SET", "t", "v", "PX", "1500"),
	testutil.S(":1500", "PTTL", "t"),
	testutil.S(":2", "TTL", "t"),
	testutil.S("+OK", "SETEX", "t", "50", "v"),
	testutil.S(":50", "TTL", "t"),
	testutil.S("+OK", "PSETEX", "t", "2500", "v"),
	testutil.S(":2500", "PTTL", "t"),
	testutil.S(`"v"`, "GETEX", "t", "EX", "30"),
	testutil.S(":30", "TTL", "t"),
	testutil.S(`"v"`, "GETEX", "t", "PX", "700"),
	testutil.S(":700", "PTTL", "t"),
	testutil.S("+OK", "SET", "t", "v", "EX", "10", "XX"),
	testutil.S(":10", "TTL", "t"),
	testutil.S("+OK", "SET", "t", "v", "EXAT", "1700000060"),
	testutil.S(":60", "TTL", "t"),
}

// concat joins scripts into one.
func concat(scripts ...[]testutil.Step) []testutil.Step {
	var out []testutil.Step
	for _, s := range scripts {
		out = append(out, s...)
	}
	return out
}

func TestStringCommands(t *testing.T) {
	fc, c := startWithClock(t, nil)
	testutil.RunScript(t, c, stringScript)
	testutil.RunScript(t, c, stringTTLScript)
	fc.Advance(61 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("(nil)", "GET", "t"),
		testutil.S(":-2", "TTL", "t"),
	})
}

func TestStringCommandsDifferential(t *testing.T) {
	testutil.Differential(t, stringScript, testutil.KnownDivergences)
}

// Redis's string2ld accepts "inf"; incrbyfloatCommand then rejects the sum.
func TestStringCommands_IncrByFloatInf(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("-ERR increment would produce NaN or Infinity", "INCRBYFLOAT", "f", "inf"),
		testutil.S("-ERR increment would produce NaN or Infinity", "INCRBYFLOAT", "f", "-inf"),
	})
}

// Redis's lcsCommand reports a non-string key with its own error, not WRONGTYPE.
func TestStringCommands_LCSWrongType(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(":1", "LPUSH", "lst", "a"),
		testutil.S("+OK", "SET", "k1", "x"),
		testutil.S("-ERR The specified keys must contain string values", "LCS", "k1", "lst"),
	})
}
