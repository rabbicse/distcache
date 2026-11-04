// Integration and differential tests for the bitmap command family
// (SETBIT, GETBIT, BITCOUNT, BITPOS, BITOP, BITFIELD, BITFIELD_RO).
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// bitmapScript covers every bitmap command.
var bitmapScript = concat(bitmapBitScript, bitmapOpScript, bitmapFieldScript)

const errBitOffset = "-ERR bit offset is not an integer or out of range"

var bitmapBitScript = []testutil.Step{
	testutil.S(":1", "LPUSH", "lst", "a"),
	testutil.S(":0", "SETBIT", "bm", "7", "1"),
	testutil.S(":1", "SETBIT", "bm", "7", "0"),
	testutil.S(":0", "SETBIT", "bm", "7", "1"),
	testutil.S(`"\x01"`, "GET", "bm"),
	testutil.S(":1", "GETBIT", "bm", "7"),
	testutil.S(":0", "GETBIT", "bm", "100"),
	testutil.S(":0", "GETBIT", "nope", "0"),
	testutil.S("-ERR bit is not an integer or out of range", "SETBIT", "bm", "7", "2"),
	testutil.S(errBitOffset, "SETBIT", "bm", "-1", "1"),
	testutil.S(errBitOffset, "SETBIT", "bm", "4294967296", "1"),
	testutil.S(errBitOffset, "GETBIT", "bm", "x"),
	testutil.S(wrongType, "SETBIT", "lst", "0", "1"),
	testutil.S(wrongType, "GETBIT", "lst", "0"),
	testutil.S(arityErr("setbit"), "SETBIT", "bm", "1"),
	testutil.S(arityErr("getbit"), "GETBIT", "bm"),
	testutil.S("+OK", "SET", "s", "foobar"),
	testutil.S(":26", "BITCOUNT", "s"),
	testutil.S(":4", "BITCOUNT", "s", "0", "0"),
	testutil.S(":6", "BITCOUNT", "s", "1", "1"),
	testutil.S(":6", "BITCOUNT", "s", "1", "1", "BYTE"),
	testutil.S(":17", "BITCOUNT", "s", "5", "30", "BIT"),
	testutil.S(":26", "BITCOUNT", "s", "0", "-1"),
	testutil.S(":0", "BITCOUNT", "s", "4", "2"),
	testutil.S(":0", "BITCOUNT", "nope"),
	testutil.S(errSyntax, "BITCOUNT", "s", "0"),
	testutil.S(errSyntax, "BITCOUNT", "s", "0", "1", "FOO"),
	testutil.S(errNotInt, "BITCOUNT", "s", "a", "b"),
	testutil.S(wrongType, "BITCOUNT", "lst"),
	testutil.S(arityErr("bitcount"), "BITCOUNT"),
	testutil.S("+OK", "SET", "bp", "\xff\xf0\x00"),
	testutil.S(":12", "BITPOS", "bp", "0"),
	testutil.S(":0", "BITPOS", "bp", "1"),
	testutil.S("+OK", "SET", "bp2", "\x00\xff\xf0"),
	testutil.S(":8", "BITPOS", "bp2", "1", "0"),
	testutil.S(":16", "BITPOS", "bp2", "1", "2"),
	testutil.S(":16", "BITPOS", "bp2", "1", "2", "-1", "BYTE"),
	testutil.S(":8", "BITPOS", "bp2", "1", "7", "15", "BIT"),
	testutil.S(":7", "BITPOS", "bp2", "0", "7", "15", "BIT"),
	testutil.S(":-1", "BITPOS", "bp2", "1", "0", "0"),
	testutil.S("+OK", "SET", "ones", "\xff\xff\xff"),
	testutil.S(":24", "BITPOS", "ones", "0"),
	testutil.S(":-1", "BITPOS", "ones", "0", "0", "-1"),
	testutil.S(":-1", "BITPOS", "ones", "0", "0", "23", "BIT"),
	testutil.S(":0", "BITPOS", "nope", "0"),
	testutil.S(":-1", "BITPOS", "nope", "1"),
	testutil.S("-ERR The bit argument must be 1 or 0.", "BITPOS", "bp", "2"),
	testutil.S(errSyntax, "BITPOS", "bp", "1", "0", "1", "FOO"),
	testutil.S(errNotInt, "BITPOS", "bp", "1", "x"),
	testutil.S(wrongType, "BITPOS", "lst", "1"),
	testutil.S(arityErr("bitpos"), "BITPOS", "bp"),
}

var bitmapOpScript = []testutil.Step{
	testutil.S("+OK", "MSET", "x1", "foobar", "x2", "abcdef"),
	testutil.S(":6", "BITOP", "AND", "d", "x1", "x2"),
	testutil.S(`"`+"`bc`ab"+`"`, "GET", "d"),
	testutil.S(":6", "BITOP", "OR", "d", "x1", "x2"),
	testutil.S(`"goofev"`, "GET", "d"),
	testutil.S(":6", "BITOP", "XOR", "d", "x1", "x2"),
	testutil.S(`"\a\r\f\x06\x04\x14"`, "GET", "d"),
	testutil.S(":6", "BITOP", "NOT", "d", "x1"),
	testutil.S(`"\x99\x90\x90\x9d\x9e\x8d"`, "GET", "d"),
	testutil.S(":6", "BITOP", "DIFF", "d", "x1", "x2"),
	testutil.S(`"\x06\r\f\x02\x00\x10"`, "GET", "d"),
	testutil.S(":6", "BITOP", "DIFF1", "d", "x1", "x2"),
	testutil.S(`"\x01\x00\x00\x04\x04\x04"`, "GET", "d"),
	testutil.S(":6", "BITOP", "ANDOR", "d", "x1", "x2"),
	testutil.S(`"`+"`bc`ab"+`"`, "GET", "d"),
	testutil.S(":6", "BITOP", "ONE", "d", "x1", "x2"),
	testutil.S(`"\a\r\f\x06\x04\x14"`, "GET", "d"),
	testutil.S(":6", "BITOP", "and", "d", "x1"),
	testutil.S(`"foobar"`, "GET", "d"),
	testutil.S("-ERR BITOP NOT must be called with a single source key.", "BITOP", "NOT", "d", "x1", "x2"),
	testutil.S("-ERR BITOP DIFF must be called with at least two source keys.", "BITOP", "DIFF", "d", "x1"),
	testutil.S(errSyntax, "BITOP", "FOO", "d", "x1"),
	testutil.S(arityErr("bitop"), "BITOP", "AND", "d"),
	testutil.S(wrongType, "BITOP", "AND", "d", "lst"),
	testutil.S(":0", "BITOP", "AND", "d", "nope"),
	testutil.S(":0", "EXISTS", "d"),
}

var bitmapFieldScript = []testutil.Step{
	testutil.S("*[:0, :255]", "BITFIELD", "bf", "SET", "u8", "0", "255", "GET", "u8", "0"),
	testutil.S("*[:9]", "BITFIELD", "bf", "INCRBY", "u8", "0", "10"),
	testutil.S("*[:255]", "BITFIELD", "bf", "OVERFLOW", "SAT", "INCRBY", "u8", "0", "300"),
	testutil.S("*[(nil)]", "BITFIELD", "bf", "OVERFLOW", "FAIL", "INCRBY", "u8", "0", "1"),
	testutil.S("*[:-56]", "BITFIELD", "bf", "OVERFLOW", "WRAP", "INCRBY", "i8", "8", "200"),
	testutil.S("*[:-128]", "BITFIELD", "bf", "OVERFLOW", "SAT", "INCRBY", "i8", "8", "-200"),
	testutil.S("*[:-128, :-5]", "BITFIELD", "bf", "SET", "i8", "#1", "-5", "GET", "i8", "#1"),
	testutil.S("*[:15]", "BITFIELD", "bf", "GET", "u4", "0"),
	testutil.S("*[:255, :0]", "BITFIELD", "bf", "GET", "u8", "0", "GET", "u8", "100"),
	testutil.S("-ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.", "BITFIELD", "bf", "GET", "u64", "0"),
	testutil.S("-ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.", "BITFIELD", "bf", "GET", "x8", "0"),
	testutil.S(errBitOffset, "BITFIELD", "bf", "GET", "u8", "-1"),
	testutil.S("-ERR Invalid OVERFLOW type specified", "BITFIELD", "bf", "OVERFLOW", "FOO", "GET", "u8", "0"),
	testutil.S(errSyntax, "BITFIELD", "bf", "FOO"),
	testutil.S(errSyntax, "BITFIELD", "bf", "GET", "u8"),
	testutil.S(errNotInt, "BITFIELD", "bf", "SET", "u8", "0", "x"),
	testutil.S("*[]", "BITFIELD", "bf"),
	testutil.S(wrongType, "BITFIELD", "lst", "GET", "u8", "0"),
	testutil.S("*[:0]", "BITFIELD", "nope", "GET", "u8", "0"),
	testutil.S(":0", "EXISTS", "nope"),
	testutil.S(arityErr("bitfield"), "BITFIELD"),
	testutil.S("*[:255]", "BITFIELD_RO", "bf", "GET", "u8", "0"),
	testutil.S("-ERR BITFIELD_RO only supports the GET subcommand", "BITFIELD_RO", "bf", "SET", "u8", "0", "1"),
	testutil.S(wrongType, "BITFIELD_RO", "lst", "GET", "u8", "0"),
	testutil.S(arityErr("bitfield_ro"), "BITFIELD_RO"),
}

func TestBitmapCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), bitmapScript)
}

func TestBitmapCommandsDifferential(t *testing.T) {
	testutil.Differential(t, bitmapScript, testutil.KnownDivergences)
}
