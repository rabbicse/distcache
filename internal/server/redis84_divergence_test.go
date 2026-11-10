// Regression tests for divergences from Redis 8.4 found by the differential
// tests (run against redis:8.4.8). Each entry was a known bug, now fixed;
// TestRedis84Divergences keeps DistCache matching the expectations, while
// TestRedis84Divergences_WantsMatchRedis checks every expectation against a
// real Redis.
package server_test

import (
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// divergence is one known bug with the Redis 8.4 replies DistCache must give.
type divergence struct {
	name string // test name suffix
	bug  string // the fixed bug: "known bug Bnn: ..."
	// steps returns the script; it may query c (for example FUNCTION DUMP)
	// because some payloads are server-specific.
	steps func(t *testing.T, c *testutil.Client) []testutil.Step
}

// fixed wraps a static script.
func fixed(steps ...testutil.Step) func(*testing.T, *testutil.Client) []testutil.Step {
	return func(*testing.T, *testutil.Client) []testutil.Step { return steps }
}

// reset clears data and functions so each script starts from an empty server.
var reset = []testutil.Step{
	testutil.S("+OK", "FLUSHALL"),
	testutil.S("+OK", "FUNCTION", "FLUSH"),
}

var redis84Divergences = []divergence{
	{"GeoCoordinateFormat", "known bug B29: GEOPOS/WITHCOORD format coordinates with %.17g instead of the shortest round-trip form", fixed(
		testutil.S(":2", "GEOADD", "g", palermoLon, palermoLat, "Palermo", cataniaLon, cataniaLat, "Catania"),
		testutil.S("*["+palermoPos+", "+cataniaPos+", (nil-array)]", "GEOPOS", "g", "Palermo", "Catania", "nope"),
		testutil.S(`*[*["Catania", "56.4413", `+cataniaPos+`], *["Palermo", "190.4424", `+palermoPos+`]]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYBOX", "400", "400", "km", "ASC", "WITHCOORD", "WITHDIST"),
		testutil.S(`*[*["Catania", `+cataniaPos+`]]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "COUNT", "1", "WITHCOORD"),
	)},
	// Captured from Redis on arm64, where GCC fuses the longitude delta in
	// geohashGetDistance; an x86-64 Redis build replies "0" here. DistCache
	// pins the arm64 rounding on every architecture (ds.GeoDistance).
	{"GeoStoreDistSelfDistance", "known bug B20: STOREDIST self-distance is 8.08e-14, Redis computes 4.31e-14 (haversine operand order)", fixed(
		testutil.S(":1", "GEOADD", "g", palermoLon, palermoLat, "Palermo"),
		testutil.S(":1", "GEORADIUSBYMEMBER", "g", "Palermo", "10", "km", "STOREDIST", "dst3"),
		testutil.S(`*["Palermo", "4.313314116504969e-14"]`, "ZRANGE", "dst3", "0", "-1", "WITHSCORES"),
	)},
	{"GeoSearchStoreWithDist", "known bug B30: GEOSEARCHSTORE ... WITHDIST error text differs", fixed(
		testutil.S(":1", "GEOADD", "gu", palermoLon, palermoLat, "Palermo"),
		testutil.S("-ERR GEOSEARCHSTORE is not compatible with WITHDIST, WITHHASH and WITHCOORD options", "GEOSEARCHSTORE", "dst", "gu", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "WITHDIST"),
	)},
	{"GeoSearchTwoOrigins", "known bug B31: GEOSEARCH with FROMMEMBER and FROMLONLAT must reply syntax error", fixed(
		testutil.S(":1", "GEOADD", "gu", palermoLon, palermoLat, "Palermo"),
		testutil.S(errSyntax, "GEOSEARCH", "gu", "FROMMEMBER", "Palermo", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"),
	)},
	{"ExpireTimeRounding", "known bug B32: EXPIRETIME truncates milliseconds; Redis rounds to the nearest second", fixed(
		testutil.S("+OK", "SET", "c", "3"),
		testutil.S(":1", "PEXPIREAT", "c", "4102444800500"),
		testutil.S(":4102444801", "EXPIRETIME", "c"),
	)},
	{"SelectNotInteger", "known bug B33: SELECT with a non-integer index replies 'invalid DB index'", fixed(
		testutil.S(errNotInt, "SELECT", "x"),
	)},
	{"BlockingMoveTimeout", "known bug B34: BLMOVE/BRPOPLPUSH timeout replies a null bulk; Redis replies a null array", fixed(
		testutil.S("(nil-array)", "BLMOVE", "b2", "b3", "LEFT", "RIGHT", "0.01"),
		testutil.S("(nil-array)", "BRPOPLPUSH", "b2", "b3", "0.01"),
	)},
	{"XReadUnbalanced", "known bug B35: XREAD unbalanced streams error text lacks the '+' ID", fixed(
		testutil.S("-ERR Unbalanced 'xread' list of streams: for each stream key an ID, '+', or '$' must be specified.", "XREAD", "STREAMS", "g", "h", "0"),
	)},
	{"ZInterCardZeroKeys", "known bug B36: ZINTERCARD 0 error text differs", fixed(
		testutil.S("-ERR at least 1 input key is needed for 'zintercard' command", "ZINTERCARD", "0", "za"),
	)},
	{"ScriptFlushBadOption", "known bug B37: SCRIPT FLUSH with an unknown option replies syntax error", fixed(
		testutil.S("-ERR SCRIPT FLUSH only support SYNC|ASYNC option", "SCRIPT", "FLUSH", "BOGUS"),
	)},
	{"FCallBadNumKeys", "known bug B38: FCALL with a non-integer numkeys replies the generic integer error", fixed(
		testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode),
		testutil.S("-ERR Bad number of keys provided", "FCALL", "f1", "x"),
	)},
	{"FunctionListFlags", "known bug B39: FUNCTION LIST returns flags as bulk strings; Redis uses status replies", fixed(
		testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode),
		testutil.S(`*[`+libListEntry+`]`, "FUNCTION", "LIST"),
		testutil.S(`*[`+libListEntry+`]`, "FUNCTION", "LIST", "LIBRARYNAME", "l*"),
	)},
	{"FunctionListBadArg", "known bug B40: FUNCTION LIST with an unknown argument replies syntax error", fixed(
		testutil.S("-ERR Unknown argument BOGUS", "FUNCTION", "LIST", "BOGUS"),
	)},
	{"FunctionRestoreDuplicate", "known bug B23: FUNCTION RESTORE duplicate library quotes the name; Redis does not",
		func(t *testing.T, c *testutil.Client) []testutil.Step {
			testutil.RunScript(t, c, []testutil.Step{testutil.S(`"lib"`, "FUNCTION", "LOAD", libCode)})
			v, err := c.Do("FUNCTION", "DUMP")
			if err != nil {
				t.Fatalf("FUNCTION DUMP: %v", err)
			}
			return []testutil.Step{testutil.S("-ERR Library lib already exists", "FUNCTION", "RESTORE", string(v.Str))}
		}},
}

// runDivergence runs d's script on c after resetting it.
func runDivergence(t *testing.T, c *testutil.Client, d divergence) {
	t.Helper()
	testutil.RunScript(t, c, reset)
	testutil.RunScript(t, c, d.steps(t, c))
}

func TestRedis84Divergences(t *testing.T) {
	for _, d := range redis84Divergences {
		t.Run(d.name, func(t *testing.T) {
			_, addr := testutil.StartServer(t, nil)
			runDivergence(t, testutil.MustDial(t, addr), d)
		})
	}
}

// TestRedis84Divergences_WantsMatchRedis proves the expectations above are
// what Redis 8.4 actually replies, so fixing a bug means matching Redis.
func TestRedis84Divergences_WantsMatchRedis(t *testing.T) {
	addr, ok := testutil.StartRedis(t)
	if !ok {
		t.Skip("redis-server not available (install it or set REDIS_SERVER_ADDR)")
	}
	c := testutil.MustDial(t, addr)
	// An external Redis is shared with later tests: leave it empty.
	t.Cleanup(func() { testutil.RunScript(t, c, reset) })
	for _, d := range redis84Divergences {
		t.Run(d.name, func(t *testing.T) { runDivergence(t, c, d) })
	}
}
