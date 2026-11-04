// Integration and differential tests for the stream command family. Expected
// replies are Redis 8.4's. The differential script uses explicit IDs only;
// auto-generated IDs and idle times are checked on a FakeClock server.
package server_test

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/testutil"
)

const errInvalidStreamID = "-ERR Invalid stream ID specified as stream command argument"

// streamScript covers the stream commands with time-independent steps.
var streamScript = concat(streamAddScript, streamTrimScript, streamGroupScript)

var streamAddScript = []testutil.Step{
	testutil.S(`"1-1"`, "XADD", "s", "1-1", "f", "a"),
	testutil.S("-ERR The ID specified in XADD is equal or smaller than the target stream top item", "XADD", "s", "1-1", "f", "x"),
	testutil.S("-ERR The ID specified in XADD is equal or smaller than the target stream top item", "XADD", "s", "1-0", "f", "x"),
	testutil.S("-ERR The ID specified in XADD must be greater than 0-0", "XADD", "z", "0-0", "f", "x"),
	testutil.S(`"2-0"`, "XADD", "s", "2-*", "f", "b"),
	testutil.S(`"2-1"`, "XADD", "s", "2-*", "f", "c"),
	testutil.S(`"3-0"`, "XADD", "s", "3", "f", "d", "g", "e"),
	testutil.S(errInvalidStreamID, "XADD", "s", "abc", "f", "x"),
	testutil.S(arityErr("xadd"), "XADD", "s", "4-0", "f", "x", "g"),
	testutil.S(arityErr("xadd"), "XADD", "s", "4-0", "f"),
	testutil.S("(nil)", "XADD", "nos", "NOMKSTREAM", "*", "f", "v"),
	testutil.S(":0", "EXISTS", "nos"),
	testutil.S(":4", "XLEN", "s"),
	testutil.S(":0", "XLEN", "missing"),
	testutil.S(`*[*["1-1", *["f", "a"]], *["2-0", *["f", "b"]], *["2-1", *["f", "c"]], *["3-0", *["f", "d", "g", "e"]]]`, "XRANGE", "s", "-", "+"),
	testutil.S(`*[*["2-0", *["f", "b"]], *["2-1", *["f", "c"]]]`, "XRANGE", "s", "(1-1", "2-1"),
	testutil.S(`*[*["2-0", *["f", "b"]], *["2-1", *["f", "c"]]]`, "XRANGE", "s", "2", "2"),
	testutil.S(`*[*["2-1", *["f", "c"]]]`, "XRANGE", "s", "(2-0", "(3-0"),
	testutil.S(`*[*["1-1", *["f", "a"]]]`, "XRANGE", "s", "-", "+", "COUNT", "1"),
	testutil.S("*[]", "XRANGE", "s", "4", "+"),
	testutil.S("*[]", "XRANGE", "missing", "-", "+"),
	testutil.S(`*[*["3-0", *["f", "d", "g", "e"]]]`, "XREVRANGE", "s", "+", "-", "COUNT", "1"),
	testutil.S(`*[*["2-1", *["f", "c"]], *["2-0", *["f", "b"]]]`, "XREVRANGE", "s", "(3-0", "(1-1"),
	testutil.S(errInvalidStreamID, "XRANGE", "s", "x", "+"),
	testutil.S("-ERR invalid start ID for the interval", "XRANGE", "s", "(18446744073709551615-18446744073709551615", "+"),
	testutil.S(errSyntax, "XRANGE", "s", "-", "+", "FOO"),
	testutil.S(":1", "XDEL", "s", "2-0"),
	testutil.S(":0", "XDEL", "s", "2-0", "9-9"),
	testutil.S(":3", "XLEN", "s"),
	testutil.S(":0", "XDEL", "missing", "1-1"),
	testutil.S(errInvalidStreamID, "XDEL", "s", "x"),
	testutil.S("-ERR The ID specified in XSETID is smaller than the target stream top item", "XSETID", "s", "2-0"),
	testutil.S("+OK", "XSETID", "s", "10-0"),
	testutil.S("-ERR The ID specified in XADD is equal or smaller than the target stream top item", "XADD", "s", "5-0", "f", "x"),
	testutil.S("-ERR The entries_added specified in XSETID is smaller than the target stream length", "XSETID", "s", "10-0", "ENTRIESADDED", "1"),
	testutil.S("-ERR The ID specified in XSETID is smaller than the provided max_deleted_entry_id", "XSETID", "s", "10-0", "ENTRIESADDED", "100", "MAXDELETEDID", "11-0"),
	testutil.S("+OK", "XSETID", "s", "10-0", "ENTRIESADDED", "100", "MAXDELETEDID", "9-0"),
	testutil.S("-ERR no such key", "XSETID", "missing", "1-0"),
	testutil.S(`"10-1"`, "XADD", "s", "10-*", "f", "h"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "XADD", "str", "1-1", "f", "v"),
	testutil.S(wrongType, "XLEN", "str"),
	testutil.S(wrongType, "XRANGE", "str", "-", "+"),
}

var streamTrimScript = []testutil.Step{
	testutil.S(`"1-0"`, "XADD", "t", "MAXLEN", "2", "1-0", "f", "1"),
	testutil.S(`"2-0"`, "XADD", "t", "MAXLEN", "2", "2-0", "f", "2"),
	testutil.S(`"3-0"`, "XADD", "t", "MAXLEN", "2", "3-0", "f", "3"),
	testutil.S(`*[*["2-0", *["f", "2"]], *["3-0", *["f", "3"]]]`, "XRANGE", "t", "-", "+"),
	testutil.S(":2", "XLEN", "t"),
	testutil.S(`"4-0"`, "XADD", "t", "MAXLEN", "=", "1", "4-0", "f", "4"),
	testutil.S(":1", "XLEN", "t"),
	// ~ only trims whole radix-tree nodes (100 entries by default), so
	// nothing is removed from a small stream.
	testutil.S(`"5-0"`, "XADD", "t", "MAXLEN", "~", "1", "5-0", "f", "5"),
	testutil.S(`"6-0"`, "XADD", "t", "MAXLEN", "~", "1", "LIMIT", "10", "6-0", "f", "6"),
	testutil.S(":3", "XLEN", "t"),
	testutil.S(":0", "XTRIM", "t", "MAXLEN", "~", "1"),
	testutil.S(":0", "XTRIM", "t", "MINID", "~", "6-0"),
	testutil.S(`"7-0"`, "XADD", "t", "MINID", "5-0", "7-0", "f", "7"),
	testutil.S(":3", "XLEN", "t"),
	testutil.S(`"8-0"`, "XADD", "t", "MINID", "=", "6-0", "8-0", "f", "8"),
	testutil.S(":1", "XTRIM", "t", "MINID", "7-0"),
	testutil.S(":1", "XTRIM", "t", "MAXLEN", "1"),
	testutil.S(`*[*["8-0", *["f", "8"]]]`, "XRANGE", "t", "-", "+"),
	testutil.S(":0", "XTRIM", "t", "MAXLEN", "5"),
	testutil.S(":0", "XTRIM", "missing", "MAXLEN", "0"),
	testutil.S("-ERR syntax error, LIMIT cannot be used without the special ~ option", "XTRIM", "t", "MAXLEN", "1", "LIMIT", "10"),
	testutil.S("-ERR syntax error, LIMIT cannot be used without the special ~ option", "XADD", "t", "MAXLEN", "1", "LIMIT", "10", "9-0", "f", "x"),
	testutil.S("-ERR The MAXLEN argument must be >= 0.", "XTRIM", "t", "MAXLEN", "-1"),
	testutil.S(errSyntax, "XTRIM", "t", "FOO", "1"),
	testutil.S(wrongType, "XTRIM", "str", "MAXLEN", "0"),
}

var streamGroupScript = []testutil.Step{
	testutil.S(`"1-0"`, "XADD", "g", "1-0", "f", "1"),
	testutil.S(`"2-0"`, "XADD", "g", "2-0", "f", "2"),
	testutil.S(`"3-0"`, "XADD", "g", "3-0", "f", "3"),
	testutil.S(`*[*["g", *[*["1-0", *["f", "1"]]]]]`, "XREAD", "COUNT", "1", "STREAMS", "g", "0"),
	testutil.S(`*[*["g", *[*["3-0", *["f", "3"]]]]]`, "XREAD", "STREAMS", "g", "2-0"),
	testutil.S(`*[*["g", *[*["3-0", *["f", "3"]]]]]`, "XREAD", "STREAMS", "g", "+"),
	testutil.S("(nil-array)", "XREAD", "STREAMS", "g", "$"),
	testutil.S("(nil-array)", "XREAD", "STREAMS", "g", "missing", "3-0", "0"),
	testutil.S("-ERR Unbalanced 'xread' list of streams: for each stream key an ID or '$' must be specified.", "XREAD", "STREAMS", "g", "h", "0"),
	testutil.S("-ERR The > ID can be specified only when calling XREADGROUP using the GROUP <group> <consumer> option.", "XREAD", "STREAMS", "g", ">"),
	testutil.S(errSyntax, "XREAD", "FOO", "STREAMS", "g", "0"),
	testutil.S("+OK", "XGROUP", "CREATE", "g", "grp", "0"),
	testutil.S("-BUSYGROUP Consumer Group name already exists", "XGROUP", "CREATE", "g", "grp", "0"),
	testutil.S("-ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.", "XGROUP", "CREATE", "nokey", "grp", "$"),
	testutil.S("+OK", "XGROUP", "CREATE", "mk", "grp", "$", "MKSTREAM"),
	testutil.S(":0", "XLEN", "mk"),
	testutil.S("+OK", "XGROUP", "CREATE", "g", "late", "$", "ENTRIESREAD", "3"),
	testutil.S(`*[*["g", *[*["1-0", *["f", "1"]], *["2-0", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "grp", "c1", "COUNT", "2", "STREAMS", "g", ">"),
	testutil.S(`*[*["g", *[*["1-0", *["f", "1"]], *["2-0", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "grp", "c1", "STREAMS", "g", "0"),
	testutil.S("(nil-array)", "XREADGROUP", "GROUP", "late", "c1", "STREAMS", "g", ">"),
	testutil.S("-NOGROUP No such key 'g' or consumer group 'nog' in XREADGROUP with GROUP option", "XREADGROUP", "GROUP", "nog", "c1", "STREAMS", "g", ">"),
	testutil.S(":1", "XACK", "g", "grp", "1-0"),
	testutil.S(":0", "XACK", "g", "grp", "1-0", "9-0"),
	testutil.S(":0", "XACK", "g", "nog", "1-0"),
	testutil.S(errInvalidStreamID, "XACK", "g", "grp", "x"),
	testutil.S(`*[:1, "2-0", "2-0", *[*["c1", "1"]]]`, "XPENDING", "g", "grp"),
	testutil.S(`*[:0, (nil), (nil), (nil-array)]`, "XPENDING", "g", "late"),
	testutil.S("-NOGROUP No such key 'g' or consumer group 'nog'", "XPENDING", "g", "nog"),
	testutil.S(`*[*["2-0", *["f", "2"]]]`, "XCLAIM", "g", "grp", "c2", "0", "2-0"),
	testutil.S(`*["2-0"]`, "XCLAIM", "g", "grp", "c3", "0", "2-0", "JUSTID"),
	testutil.S("*[]", "XCLAIM", "g", "grp", "c3", "0", "9-0"),
	testutil.S(`*[:1, "2-0", "2-0", *[*["c3", "1"]]]`, "XPENDING", "g", "grp"),
	testutil.S(`*["0-0", *["2-0"], *[]]`, "XAUTOCLAIM", "g", "grp", "c4", "0", "0-0", "JUSTID"),
	testutil.S(`*["0-0", *[*["2-0", *["f", "2"]]], *[]]`, "XAUTOCLAIM", "g", "grp", "c1", "0", "0-0", "COUNT", "10"),
	testutil.S("-ERR COUNT must be > 0", "XAUTOCLAIM", "g", "grp", "c1", "0", "0-0", "COUNT", "0"),
	testutil.S(":1", "XGROUP", "CREATECONSUMER", "g", "grp", "c9"),
	testutil.S(":0", "XGROUP", "CREATECONSUMER", "g", "grp", "c9"),
	testutil.S(":0", "XGROUP", "DELCONSUMER", "g", "grp", "c9"),
	testutil.S(":1", "XGROUP", "DELCONSUMER", "g", "grp", "c1"),
	testutil.S(":0", "XGROUP", "DELCONSUMER", "g", "grp", "nobody"),
	testutil.S(`*[:0, (nil), (nil), (nil-array)]`, "XPENDING", "g", "grp"),
	testutil.S("+OK", "XGROUP", "SETID", "g", "grp", "0"),
	testutil.S(`*[*["g", *[*["1-0", *["f", "1"]]]]]`, "XREADGROUP", "GROUP", "grp", "c5", "COUNT", "1", "STREAMS", "g", ">"),
	testutil.S("+OK", "XGROUP", "SETID", "g", "grp", "$", "ENTRIESREAD", "3"),
	testutil.S("(nil-array)", "XREADGROUP", "GROUP", "grp", "c5", "STREAMS", "g", ">"),
	testutil.S(":1", "XGROUP", "DESTROY", "g", "late"),
	testutil.S(":0", "XGROUP", "DESTROY", "g", "late"),
	testutil.S(wrongType, "XGROUP", "CREATE", "str", "grp", "0"),
	testutil.S(wrongType, "XREAD", "STREAMS", "str", "0"),
}

func TestStreamCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), streamScript)
}

func TestStreamCommandsDifferential(t *testing.T) {
	testutil.Differential(t, streamScript, testutil.KnownDivergences)
}

// TestStreamCommands_FakeClock checks auto IDs, idle times and the XINFO
// replies, all of which depend on the clock.
func TestStreamCommands_FakeClock(t *testing.T) {
	fc, c := startWithClock(t, nil)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`"1700000000000-0"`, "XADD", "s", "*", "f", "1"),
		testutil.S(`"1700000000000-1"`, "XADD", "s", "*", "f", "2"),
		testutil.S("+OK", "XGROUP", "CREATE", "s", "g", "0"),
		testutil.S(`*[*["s", *[*["1700000000000-0", *["f", "1"]], *["1700000000000-1", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "g", "c1", "STREAMS", "s", ">"),
	})
	fc.Advance(5 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`"1700000005000-0"`, "XADD", "s", "*", "f", "3"),
		testutil.S(`*[*["1700000000000-0", "c1", :5000, :1], *["1700000000000-1", "c1", :5000, :1]]`, "XPENDING", "s", "g", "-", "+", "10"),
		testutil.S(`*[*["1700000000000-0", "c1", :5000, :1]]`, "XPENDING", "s", "g", "IDLE", "4000", "-", "+", "1", "c1"),
		testutil.S("*[]", "XPENDING", "s", "g", "IDLE", "6000", "-", "+", "10"),
		testutil.S("*[]", "XPENDING", "s", "g", "-", "+", "10", "nobody"),
		testutil.S(`*["1700000000000-0"]`, "XCLAIM", "s", "g", "c2", "4000", "1700000000000-0", "JUSTID"),
		testutil.S("*[]", "XCLAIM", "s", "g", "c2", "4000", "1700000000000-0"),
		testutil.S(`*[*["1700000000000-0", "c2", :0, :1], *["1700000000000-1", "c1", :5000, :1]]`, "XPENDING", "s", "g", "-", "+", "10"),
		testutil.S(`*["0-0", *["1700000000000-1"], *[]]`, "XAUTOCLAIM", "s", "g", "c3", "4000", "0-0", "JUSTID"),
		testutil.S(`*["0-0", *[], *[]]`, "XAUTOCLAIM", "s", "g", "c3", "4000", "0-0"),
	})
	fc.Advance(2 * time.Second)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`*[*["name", "g", "consumers", :3, "pending", :2, "last-delivered-id", "1700000000000-1", "entries-read", :2, "lag", :1]]`, "XINFO", "GROUPS", "s"),
		testutil.S(`*[*["name", "c1", "pending", :0, "idle", :7000, "inactive", :7000], *["name", "c2", "pending", :1, "idle", :2000, "inactive", :2000], *["name", "c3", "pending", :1, "idle", :2000, "inactive", :2000]]`, "XINFO", "CONSUMERS", "s", "g"),
		testutil.S("-NOGROUP No such consumer group 'nog' for key name 's'", "XINFO", "CONSUMERS", "s", "nog"),
		testutil.S("-ERR no such key", "XINFO", "STREAM", "missing"),
	})
}

// replyMap turns a flat RESP2 key/value array into a map.
func replyMap(t *testing.T, v resp.Value) map[string]resp.Value {
	t.Helper()
	if len(v.Elems)%2 != 0 {
		t.Fatalf("got %d elements, want an even count", len(v.Elems))
	}
	m := make(map[string]resp.Value, len(v.Elems)/2)
	for i := 0; i < len(v.Elems); i += 2 {
		m[string(v.Elems[i].Str)] = v.Elems[i+1]
	}
	return m
}

// TestStreamCommands_XInfoStream checks the XINFO STREAM fields whose values
// do not depend on internal radix-tree layout.
func TestStreamCommands_XInfoStream(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`"1-0"`, "XADD", "s", "1-0", "f", "1"),
		testutil.S(`"2-0"`, "XADD", "s", "2-0", "f", "2"),
		testutil.S(":1", "XDEL", "s", "1-0"),
		testutil.S("+OK", "XGROUP", "CREATE", "s", "g", "0"),
	})
	testutil.RecordCommands(t)("xinfo")
	tests := []struct {
		name string
		cmd  []string
		want map[string]string
	}{
		{"summary", []string{"XINFO", "STREAM", "s"}, map[string]string{
			"length":                  ":1",
			"last-generated-id":       `"2-0"`,
			"max-deleted-entry-id":    `"1-0"`,
			"entries-added":           ":2",
			"recorded-first-entry-id": `"2-0"`,
			"groups":                  ":1",
			"first-entry":             `*["2-0", *["f", "2"]]`,
			"last-entry":              `*["2-0", *["f", "2"]]`,
		}},
		{"full", []string{"XINFO", "STREAM", "s", "FULL"}, map[string]string{
			"length":            ":1",
			"last-generated-id": `"2-0"`,
			"entries-added":     ":2",
			"entries":           `*[*["2-0", *["f", "2"]]]`,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := c.Do(tc.cmd...)
			if err != nil {
				t.Fatalf("%v: %v", tc.cmd, err)
			}
			m := replyMap(t, v)
			for k, want := range tc.want {
				got, ok := m[k]
				if !ok {
					t.Fatalf("%v: missing field %q", tc.cmd, k)
				}
				if r := testutil.Render(got, false); r != want {
					t.Errorf("%v %s: got %s, want %s", tc.cmd, k, r, want)
				}
			}
		})
	}
	v, err := c.Do("XINFO", "STREAM", "s", "FULL")
	if err != nil {
		t.Fatalf("XINFO STREAM FULL: %v", err)
	}
	groups := replyMap(t, v)["groups"]
	if len(groups.Elems) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups.Elems))
	}
	g := replyMap(t, groups.Elems[0])
	for _, k := range []string{"name", "last-delivered-id", "entries-read", "lag", "pel-count", "pending", "consumers"} {
		if _, ok := g[k]; !ok {
			t.Errorf("XINFO STREAM FULL group: missing field %q (have %v)", k, slices.Sorted(maps.Keys(g)))
		}
	}
}

func TestStreamCommands_XReadBlock(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c, other := testutil.MustDial(t, addr), testutil.MustDial(t, addr)
	testutil.RunScript(t, other, []testutil.Step{
		testutil.S(`"1-0"`, "XADD", "s", "1-0", "f", "1"),
		testutil.S("+OK", "XGROUP", "CREATE", "s", "g", "$"),
	})
	sendBlocking(t, c, "XREAD", "COUNT", "5", "BLOCK", "0", "STREAMS", "s", "$")
	waitBlocked(t, other, 1)
	testutil.RunScript(t, other, []testutil.Step{testutil.S(`"2-0"`, "XADD", "s", "2-0", "f", "2")})
	readReply(t, c, `*[*["s", *[*["2-0", *["f", "2"]]]]]`)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S(`*[*["s", *[*["2-0", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "g", "c1", "BLOCK", "0", "STREAMS", "s", ">"),
	})
	sendBlocking(t, c, "XREADGROUP", "GROUP", "g", "c1", "BLOCK", "0", "STREAMS", "s", ">")
	waitBlocked(t, other, 1)
	testutil.RunScript(t, other, []testutil.Step{testutil.S(`"3-0"`, "XADD", "s", "3-0", "f", "3")})
	readReply(t, c, `*[*["s", *[*["3-0", *["f", "3"]]]]]`)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("(nil-array)", "XREAD", "BLOCK", "10", "STREAMS", "s", "$"),
		testutil.S("(nil-array)", "XREADGROUP", "GROUP", "g", "c1", "BLOCK", "10", "STREAMS", "s", ">"),
	})
}

// Redis's streamParseAddOrTrimArgs rejects a second trim strategy with its
// own message.
func TestStreamCommands_MaxlenAndMinid(t *testing.T) {
	t.Skip("known bug B14: XADD/XTRIM with both MAXLEN and MINID reply plain 'syntax error' (cmd_stream.go:190 parseTrim)")
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(`"1-0"`, "XADD", "t", "1-0", "f", "1"),
		testutil.S("-ERR syntax error, MAXLEN and MINID options at the same time are not compatible", "XTRIM", "t", "MAXLEN", "1", "MINID", "1"),
		testutil.S("-ERR syntax error, MAXLEN and MINID options at the same time are not compatible", "XADD", "t", "MAXLEN", "1", "MINID", "1", "2-0", "f", "2"),
	})
}

// Redis's XREADGROUP returns pending entries with IDs strictly greater than
// the given ID.
func TestStreamCommands_XReadGroupHistoryExclusive(t *testing.T) {
	t.Skip("known bug B15: XREADGROUP with an explicit ID includes the PEL entry equal to that ID (cmd_stream.go:741 PendingRange starts at st.ids[j])")
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(`"1-0"`, "XADD", "g", "1-0", "f", "1"),
		testutil.S(`"2-0"`, "XADD", "g", "2-0", "f", "2"),
		testutil.S("+OK", "XGROUP", "CREATE", "g", "grp", "0"),
		testutil.S(`*[*["g", *[*["1-0", *["f", "1"]], *["2-0", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "grp", "c1", "STREAMS", "g", ">"),
		testutil.S(`*[*["g", *[*["2-0", *["f", "2"]]]]]`, "XREADGROUP", "GROUP", "grp", "c1", "STREAMS", "g", "1-0"),
	})
}

// Redis's xgroupCommand reports a missing group for SETID, CREATECONSUMER and
// DELCONSUMER with its own NOGROUP text.
func TestStreamCommands_XGroupNoGroupText(t *testing.T) {
	t.Skip("known bug B16: XGROUP SETID/CREATECONSUMER/DELCONSUMER on a missing group use the XPENDING NOGROUP text (cmd_stream.go:906,961,976 getGroup)")
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(`"1-0"`, "XADD", "g", "1-0", "f", "1"),
		testutil.S("-NOGROUP No such consumer group 'nog' for key name 'g'", "XGROUP", "SETID", "g", "nog", "0"),
		testutil.S("-NOGROUP No such consumer group 'nog' for key name 'g'", "XGROUP", "CREATECONSUMER", "g", "nog", "c"),
		testutil.S("-NOGROUP No such consumer group 'nog' for key name 'g'", "XGROUP", "DELCONSUMER", "g", "nog", "c"),
	})
}
