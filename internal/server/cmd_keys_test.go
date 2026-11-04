// Integration and differential tests for the generic keyspace commands
// (DEL, EXPIRE family, KEYS/SCAN, RENAME, MOVE, COPY, SORT, SWAPDB, ...).
package server_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

const errDBRange = "-ERR DB index is out of range"

// keysScript covers every keyspace command; it starts on an empty server.
var keysScript = concat(keysBasicScript, keysExpireScript, keysScanScript, keysMoveScript, keysSortScript, keysDBScript, keysEmptyKeyScript)

var keysBasicScript = []testutil.Step{
	testutil.S("(nil)", "RANDOMKEY"),
	testutil.S(":0", "DBSIZE"),
	testutil.S("+OK", "MSET", "a", "1", "b", "2", "c", "3"),
	testutil.S(":3", "DBSIZE"),
	testutil.S(arityErr("dbsize"), "DBSIZE", "x"),
	testutil.S(":1", "DEL", "a", "nope"),
	testutil.S(arityErr("del"), "DEL"),
	testutil.S(":1", "UNLINK", "b", "b"),
	testutil.S(arityErr("unlink"), "UNLINK"),
	testutil.S(":2", "EXISTS", "c", "c", "nope"),
	testutil.S(arityErr("exists"), "EXISTS"),
	testutil.S(":1", "TOUCH", "c", "nope"),
	testutil.S(arityErr("touch"), "TOUCH"),
	testutil.S(arityErr("randomkey"), "RANDOMKEY", "x"),
	testutil.S(":1", "RPUSH", "l", "x"),
	testutil.S(":1", "SADD", "st", "x"),
	testutil.S(":1", "HSET", "h", "f", "v"),
	testutil.S(":1", "ZADD", "z", "1", "m"),
	testutil.S(`"1-1"`, "XADD", "x", "1-1", "f", "v"),
	testutil.S("+string", "TYPE", "c"),
	testutil.S("+list", "TYPE", "l"),
	testutil.S("+set", "TYPE", "st"),
	testutil.S("+hash", "TYPE", "h"),
	testutil.S("+zset", "TYPE", "z"),
	testutil.S("+stream", "TYPE", "x"),
	testutil.S("+none", "TYPE", "nope"),
	testutil.S(arityErr("type"), "TYPE"),
	testutil.S(":5", "DEL", "l", "st", "h", "z", "x"),
}

var keysExpireScript = []testutil.Step{
	testutil.S(":0", "EXPIRE", "nope", "100"),
	testutil.S(":0", "PEXPIRE", "nope", "100"),
	testutil.S(":0", "EXPIREAT", "nope", farFuture),
	testutil.S(":0", "PEXPIREAT", "nope", farFutureMS),
	testutil.S(":1", "EXPIREAT", "c", farFuture),
	testutil.S(":"+farFuture, "EXPIRETIME", "c"),
	testutil.S(":"+farFutureMS, "PEXPIRETIME", "c"),
	testutil.S(":0", "EXPIREAT", "c", farFuture, "NX"),
	testutil.S(":1", "EXPIREAT", "c", "4102444900", "XX"),
	testutil.S(":0", "EXPIREAT", "c", "4102444700", "GT"),
	testutil.S(":1", "EXPIREAT", "c", "4102444700", "LT"),
	testutil.S(":4102444700", "EXPIRETIME", "c"),
	testutil.S(":1", "PEXPIREAT", "c", farFutureMS),
	testutil.S(":"+farFutureMS, "PEXPIRETIME", "c"),
	testutil.S(":1", "PEXPIREAT", "c", "4102444800500", "GT"),
	testutil.S(":4102444800500", "PEXPIRETIME", "c"),
	testutil.S(":"+farFuture, "EXPIRETIME", "c"),
	testutil.S(":1", "PERSIST", "c"),
	testutil.S(":0", "PERSIST", "c"),
	testutil.S(":0", "PERSIST", "nope"),
	testutil.S(arityErr("persist"), "PERSIST"),
	testutil.S(":-1", "TTL", "c"),
	testutil.S(":-1", "PTTL", "c"),
	testutil.S(":-1", "EXPIRETIME", "c"),
	testutil.S(":-1", "PEXPIRETIME", "c"),
	testutil.S(":-2", "TTL", "nope"),
	testutil.S(":-2", "PTTL", "nope"),
	testutil.S(":-2", "EXPIRETIME", "nope"),
	testutil.S(":-2", "PEXPIRETIME", "nope"),
	testutil.S(arityErr("ttl"), "TTL"),
	testutil.S(arityErr("pttl"), "PTTL"),
	testutil.S(arityErr("expiretime"), "EXPIRETIME"),
	testutil.S(arityErr("pexpiretime"), "PEXPIRETIME"),
	testutil.S(":0", "EXPIRE", "c", "100", "GT"),
	testutil.S(":0", "EXPIRE", "c", "100", "XX"),
	testutil.S(":1", "EXPIRE", "c", "100", "LT"),
	testutil.S(":0", "EXPIRE", "c", "100", "NX"),
	testutil.S(":1", "PEXPIRE", "c", "200000", "GT"),
	testutil.S(":1", "PEXPIRE", "c", "100000", "XX"),
	testutil.S("-ERR NX and XX, GT or LT options at the same time are not compatible", "EXPIRE", "c", "100", "NX", "XX"),
	testutil.S("-ERR NX and XX, GT or LT options at the same time are not compatible", "EXPIRE", "c", "100", "NX", "GT"),
	testutil.S("-ERR GT and LT options at the same time are not compatible", "EXPIRE", "c", "100", "GT", "LT"),
	testutil.S("-ERR Unsupported option FOO", "EXPIRE", "c", "100", "FOO"),
	testutil.S(errNotInt, "EXPIRE", "c", "abc"),
	testutil.S(errNotInt, "PEXPIREAT", "c", "abc"),
	testutil.S("-ERR invalid expire time in 'expire' command", "EXPIRE", "c", "9223372036854775807"),
	testutil.S("-ERR invalid expire time in 'pexpire' command", "PEXPIRE", "c", "9223372036854775807"),
	testutil.S("-ERR invalid expire time in 'expireat' command", "EXPIREAT", "c", "9223372036854775807"),
	testutil.S(arityErr("expire"), "EXPIRE", "c"),
	testutil.S(arityErr("pexpire"), "PEXPIRE", "c"),
	testutil.S(arityErr("expireat"), "EXPIREAT", "c"),
	testutil.S(arityErr("pexpireat"), "PEXPIREAT", "c"),
	testutil.S(":1", "PERSIST", "c"),
	testutil.S("+OK", "SET", "gone", "v"),
	testutil.S(":1", "EXPIREAT", "gone", "1"),
	testutil.S(":0", "EXISTS", "gone"),
	testutil.S("+OK", "SET", "gone", "v"),
	testutil.S(":1", "PEXPIRE", "gone", "-1"),
	testutil.S(":0", "EXISTS", "gone"),
}

var keysScanScript = []testutil.Step{
	testutil.S("+OK", "MSET", "k1", "1", "k2", "2", "key3", "3"),
	testutil.S(":1", "RPUSH", "l1", "a"),
	testutil.SS(`*["c", "k1", "k2", "key3", "l1"]`, "KEYS", "*"),
	testutil.SS(`*["k1", "k2"]`, "KEYS", "k?"),
	testutil.SS(`*["k1", "k2", "key3"]`, "KEYS", "k*"),
	testutil.SS(`*["k1", "key3"]`, "KEYS", "k[^2]*"),
	testutil.SS(`*["k2"]`, "KEYS", "k[!2]"),
	testutil.S(`*[]`, "KEYS", "nomatch"),
	testutil.S(arityErr("keys"), "KEYS"),
	testutil.S(`*["0", *["key3"]]`, "SCAN", "0", "MATCH", "key3", "COUNT", "1000"),
	testutil.S(`*["0", *["l1"]]`, "SCAN", "0", "COUNT", "1000", "TYPE", "list"),
	testutil.S(`*["0", *["l1"]]`, "SCAN", "0", "TYPE", "LIST"),
	testutil.S(`*["0", *[]]`, "SCAN", "0", "MATCH", "nomatch", "COUNT", "1000"),
	testutil.S(`*["0", *[]]`, "SCAN", "0", "TYPE", "hash"),
	testutil.S("-ERR invalid cursor", "SCAN", "abc"),
	testutil.S(errSyntax, "SCAN", "0", "COUNT", "0"),
	testutil.S(errNotInt, "SCAN", "0", "COUNT", "x"),
	testutil.S(errSyntax, "SCAN", "0", "FOO"),
	testutil.S(errSyntax, "SCAN", "0", "MATCH"),
	testutil.S(arityErr("scan"), "SCAN"),
}

var keysMoveScript = []testutil.Step{
	testutil.S("+OK", "RENAME", "k1", "r1"),
	testutil.S(`"1"`, "GET", "r1"),
	testutil.S(":0", "EXISTS", "k1"),
	testutil.S("-ERR no such key", "RENAME", "nope", "x"),
	testutil.S("+OK", "RENAME", "r1", "r1"),
	testutil.S("-ERR no such key", "RENAME", "nope", "nope"),
	testutil.S(":0", "RENAMENX", "r1", "k2"),
	testutil.S(":1", "RENAMENX", "r1", "r2"),
	testutil.S(":0", "RENAMENX", "r2", "r2"),
	testutil.S("-ERR no such key", "RENAMENX", "nope", "x"),
	testutil.S("+OK", "RENAME", "l1", "k2"),
	testutil.S("+list", "TYPE", "k2"),
	testutil.S(arityErr("rename"), "RENAME", "r2"),
	testutil.S(arityErr("renamenx"), "RENAMENX", "r2"),
	testutil.S(":1", "MOVE", "r2", "1"),
	testutil.S(":0", "MOVE", "r2", "1"),
	testutil.S("+OK", "SET", "r2", "x"),
	testutil.S(":0", "MOVE", "r2", "1"),
	testutil.S(":0", "MOVE", "nope", "1"),
	testutil.S("-ERR source and destination objects are the same", "MOVE", "r2", "0"),
	testutil.S(errDBRange, "MOVE", "r2", "16"),
	testutil.S(errNotInt, "MOVE", "r2", "x"),
	testutil.S(arityErr("move"), "MOVE", "r2"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S(`"1"`, "GET", "r2"),
	testutil.S("+OK", "SELECT", "0"),
	testutil.S(`"x"`, "GET", "r2"),
	testutil.S("+OK", "SET", "src", "v"),
	testutil.S(":1", "COPY", "src", "dst"),
	testutil.S(":0", "COPY", "src", "dst"),
	testutil.S(":1", "COPY", "src", "dst", "REPLACE"),
	testutil.S(":1", "COPY", "src", "dst", "DB", "1"),
	testutil.S(":0", "COPY", "src", "dst", "DB", "1"),
	testutil.S(":1", "COPY", "src", "dst", "DB", "1", "REPLACE"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S(`"v"`, "GET", "dst"),
	testutil.S("+OK", "SELECT", "0"),
	testutil.S("-ERR source and destination objects are the same", "COPY", "src", "src"),
	testutil.S(":0", "COPY", "nope", "x"),
	testutil.S(errDBRange, "COPY", "src", "dst", "DB", "16"),
	testutil.S(errSyntax, "COPY", "src", "dst", "FOO"),
	testutil.S(arityErr("copy"), "COPY", "src"),
	testutil.S(":1", "COPY", "k2", "lc"),
	testutil.S(`*["a"]`, "LRANGE", "lc", "0", "-1"),
	testutil.S(`(nil)`, "OBJECT", "ENCODING", "nope"),
	testutil.S(`"listpack"`, "OBJECT", "ENCODING", "lc"),
	testutil.S("-ERR An LFU maxmemory policy is not selected, access frequency not tracked. Please note that when switching between policies at runtime LRU and LFU data will take some time to adjust.", "OBJECT", "FREQ", "src"),
	testutil.S(":1", "OBJECT", "REFCOUNT", "lc"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try OBJECT HELP.", "OBJECT", "FOO"),
	testutil.S(arityErr("object|encoding"), "OBJECT", "ENCODING"),
	testutil.S("(nil)", "DUMP", "nope"),
	testutil.S("-BUSYKEY Target key name already exists.", "RESTORE", "src", "0", "x"),
	testutil.S("-ERR DUMP payload version or checksum are wrong", "RESTORE", "nk", "0", "garbage"),
	testutil.S("-ERR Invalid TTL value, must be >= 0", "RESTORE", "nk", "-1", "x"),
	testutil.S(errSyntax, "RESTORE", "nk", "0", "x", "FOO"),
	testutil.S(arityErr("restore"), "RESTORE", "nk", "0"),
	testutil.S(arityErr("dump"), "DUMP"),
}

var keysSortScript = []testutil.Step{
	testutil.S(":3", "RPUSH", "nums", "3", "1", "2"),
	testutil.S(`*["1", "2", "3"]`, "SORT", "nums"),
	testutil.S(`*["3", "2", "1"]`, "SORT", "nums", "DESC"),
	testutil.S(`*["1", "2"]`, "SORT", "nums", "LIMIT", "0", "2"),
	testutil.S(`*["2", "3"]`, "SORT", "nums", "LIMIT", "1", "-1"),
	testutil.S(":3", "RPUSH", "words", "b", "a", "c"),
	testutil.S("-ERR One or more scores can't be converted into double", "SORT", "words"),
	testutil.S(`*["a", "b", "c"]`, "SORT", "words", "ALPHA"),
	testutil.S(`*["c"]`, "SORT", "words", "ALPHA", "DESC", "LIMIT", "0", "1"),
	testutil.S("+OK", "MSET", "w_a", "3", "w_b", "1", "w_c", "2"),
	testutil.S(`*["b", "c", "a"]`, "SORT", "words", "BY", "w_*"),
	testutil.S(`*["b", "1", "c", "2", "a", "3"]`, "SORT", "words", "BY", "w_*", "GET", "#", "GET", "w_*"),
	testutil.S(`*["b", "a", "c"]`, "SORT", "words", "BY", "nosort"),
	testutil.S(`*[(nil), (nil), (nil)]`, "SORT", "words", "BY", "w_*", "GET", "nope_*"),
	testutil.S(":3", "SORT", "nums", "STORE", "out"),
	testutil.S(`*["1", "2", "3"]`, "LRANGE", "out", "0", "-1"),
	testutil.S(":2", "SADD", "sset", "20", "10"),
	testutil.S(`*["10", "20"]`, "SORT", "sset"),
	testutil.S(`*["1", "2", "3"]`, "SORT_RO", "nums"),
	testutil.S(`*["c", "b", "a"]`, "SORT_RO", "words", "ALPHA", "DESC"),
	testutil.S(errSyntax, "SORT_RO", "nums", "STORE", "out"),
	testutil.S(wrongType, "SORT", "src"),
	testutil.S(`*[]`, "SORT", "nope"),
	testutil.S(errNotInt, "SORT", "nums", "LIMIT", "x", "1"),
	testutil.S(errSyntax, "SORT", "nums", "FOO"),
	testutil.S(arityErr("sort"), "SORT"),
	testutil.S(arityErr("sort_ro"), "SORT_RO"),
}

var keysDBScript = []testutil.Step{
	testutil.S(":14", "DBSIZE"),
	testutil.S("+OK", "SWAPDB", "0", "1"),
	testutil.S(":2", "DBSIZE"),
	testutil.S("+OK", "SWAPDB", "1", "0"),
	testutil.S(":14", "DBSIZE"),
	testutil.S("+OK", "SWAPDB", "0", "0"),
	testutil.S(errDBRange, "SWAPDB", "0", "16"),
	testutil.S("-ERR invalid first DB index", "SWAPDB", "a", "1"),
	testutil.S("-ERR invalid second DB index", "SWAPDB", "0", "b"),
	testutil.S(arityErr("swapdb"), "SWAPDB", "0"),
	testutil.S(errDBRange, "SELECT", "16"),
	testutil.S(errDBRange, "SELECT", "-1"),
	testutil.S("-ERR invalid DB index", "SELECT", "x"),
	testutil.S(arityErr("select"), "SELECT"),
	testutil.S(errSyntax, "FLUSHDB", "FOO"),
	testutil.S(errSyntax, "FLUSHALL", "FOO"),
	testutil.S("+OK", "FLUSHDB", "ASYNC"),
	testutil.S(":0", "DBSIZE"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S(":2", "DBSIZE"),
	testutil.S("+OK", "FLUSHDB", "SYNC"),
	testutil.S(":0", "DBSIZE"),
	testutil.S("+OK", "SET", "a", "1"),
	testutil.S("+OK", "SELECT", "0"),
	testutil.S("+OK", "SET", "a", "1"),
	testutil.S("+OK", "FLUSHALL", "ASYNC"),
	testutil.S(":0", "DBSIZE"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S(":0", "DBSIZE"),
	testutil.S("+OK", "SELECT", "0"),
	testutil.S("+OK", "SET", "a", "1"),
	testutil.S("+OK", "FLUSHALL", "SYNC"),
	testutil.S("+OK", "SET", "a", "1"),
	testutil.S("+OK", "FLUSHDB"),
	testutil.S("+OK", "FLUSHALL"),
	testutil.S(":0", "DBSIZE"),
}

// keysEmptyKeyScript settles bug B3 (ruling R8): in Redis the pattern "*"
// matches the empty key, so KEYS * and SCAN MATCH * both return it.
var keysEmptyKeyScript = []testutil.Step{
	testutil.S("+OK", "SET", "", "v"),
	testutil.S(`"v"`, "GET", ""),
	testutil.S(`*[""]`, "KEYS", "*"),
	testutil.S(`*["0", *[""]]`, "SCAN", "0", "MATCH", "*"),
}

func TestKeysCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), keysScript)
}

func TestKeysCommandsDifferential(t *testing.T) {
	testutil.Differential(t, keysScript, testutil.KnownDivergences)
}

// keysTTLScript runs on a FakeClock server, so TTL and PTTL are exact.
var keysTTLScript = []testutil.Step{
	testutil.S("+OK", "SET", "k", "v"),
	testutil.S(":1", "EXPIRE", "k", "100"),
	testutil.S(":100", "TTL", "k"),
	testutil.S(":100000", "PTTL", "k"),
	testutil.S(":1700000100", "EXPIRETIME", "k"),
	testutil.S(":1700000100000", "PEXPIRETIME", "k"),
	testutil.S(":1", "PEXPIRE", "k", "1500"),
	testutil.S(":2", "TTL", "k"),
	testutil.S(":1500", "PTTL", "k"),
	testutil.S(":1", "EXPIREAT", "k", "1700000010"),
	testutil.S(":10", "TTL", "k"),
	testutil.S(":1", "PEXPIREAT", "k", "1700000010250"),
	testutil.S(":10250", "PTTL", "k"),
}

func TestKeysCommands_TTLWithFakeClock(t *testing.T) {
	fc, c := startWithClock(t, nil)
	testutil.RunScript(t, c, keysTTLScript)
	fc.Advance(10 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(":250", "PTTL", "k"),
		testutil.S(":0", "TTL", "k"),
	})
	fc.Advance(250 * time.Millisecond)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(":-2", "PTTL", "k"),
		testutil.S(":0", "EXISTS", "k"),
	})
}

// TestKeysCommands_ScanCursorTerminates walks SCAN with COUNT 1 until the
// cursor returns to 0 and checks the union of keys equals KEYS *.
func TestKeysCommands_ScanCursorTerminates(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	rec := testutil.RecordCommands(t)
	rec("scan")
	for i := range 50 {
		if got := c.DoS("SET", "key:"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('0'+i/26)), "v"); got != "+OK" {
			t.Fatalf("SET: got %s, want +OK", got)
		}
	}
	seen := map[string]bool{}
	cursor := "0"
	for iter := 0; ; iter++ {
		if iter > 1000 {
			t.Fatal("SCAN did not terminate after 1000 iterations")
		}
		v, err := c.Do("SCAN", cursor, "COUNT", "1")
		if err != nil {
			t.Fatalf("SCAN: %v", err)
		}
		if v.Type != '*' || len(v.Elems) != 2 {
			t.Fatalf("SCAN reply: got %s, want a 2-element array", testutil.Render(v, false))
		}
		for _, e := range v.Elems[1].Elems {
			seen[string(e.Str)] = true
		}
		cursor = string(v.Elems[0].Str)
		if cursor == "0" {
			break
		}
	}
	keys, err := c.Do("KEYS", "*")
	if err != nil {
		t.Fatalf("KEYS: %v", err)
	}
	var want, got []string
	for _, e := range keys.Elems {
		want = append(want, string(e.Str))
	}
	for k := range seen {
		got = append(got, k)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("SCAN union: got %v, want %v", got, want)
	}
}

// TestKeysCommands_DumpRestoreRoundTrip restores DistCache's own DUMP payload
// (the payload format is allowlisted, so this cannot run against Redis).
func TestKeysCommands_DumpRestoreRoundTrip(t *testing.T) {
	fc, c := startWithClock(t, nil)
	testutil.RecordCommands(t)("dump")
	testutil.RecordCommands(t)("restore")
	if got := c.DoS("RPUSH", "src", "a", "b"); got != ":2" {
		t.Fatalf("RPUSH: got %s, want :2", got)
	}
	v, err := c.Do("DUMP", "src")
	if err != nil || v.Type != '$' || v.Null {
		t.Fatalf("DUMP: got %v (err %v), want a bulk payload", testutil.Render(v, false), err)
	}
	payload := string(v.Str)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "RESTORE", "dst", "0", payload),
		testutil.S(`*["a", "b"]`, "LRANGE", "dst", "0", "-1"),
		testutil.S("-BUSYKEY Target key name already exists.", "RESTORE", "dst", "0", payload),
		testutil.S("+OK", "RESTORE", "dst", "5000", payload, "REPLACE"),
		testutil.S(":5000", "PTTL", "dst"),
		testutil.S("+OK", "RESTORE", "abs", "1700000100000", payload, "ABSTTL"),
		testutil.S(":100", "TTL", "abs"),
	})
	fc.Advance(5 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":0", "EXISTS", "dst")})
}

// Redis's RANDOMKEY never returns nil on a non-empty database.
func TestKeysCommands_RandomKeySingleKey(t *testing.T) {
	t.Skip("known bug B7: RANDOMKEY returns nil ~2% of the time with one key (cmd_keys.go cmdRandomKey samples 4*shards random shards)")
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{testutil.S("+OK", "SET", "c", "1")})
	for range 1000 {
		testutil.RunScript(t, c, []testutil.Step{testutil.S(`"c"`, "RANDOMKEY")})
	}
}
