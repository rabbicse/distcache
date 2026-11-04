// Integration and differential tests for the HyperLogLog and geo command
// families. Expected replies are Redis 8.4's; the same scripts run against
// real Redis in CI through testutil.Differential.
package server_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// corruptSparseHLL is a sparse HLL header with an invalidated cardinality
// cache followed by one ZERO opcode, so its registers do not add up to 16384.
const corruptSparseHLL = "HYLL\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x80\x00"

// hllBulkAdd is a PFADD of n distinct elements, enough to promote the HLL
// from the sparse to the dense encoding.
func hllBulkAdd(key string, n int) []string {
	cmd := []string{"PFADD", key}
	for i := range n {
		cmd = append(cmd, "e"+strconv.Itoa(i))
	}
	return cmd
}

// hllScript covers the HyperLogLog commands.
var hllScript = []testutil.Step{
	testutil.S(":1", "PFADD", "h", "a", "b", "c"),
	testutil.S(":0", "PFADD", "h", "a", "b"),
	testutil.S(":3", "PFCOUNT", "h"),
	testutil.S(":1", "PFADD", "h2", "c", "d", "e"),
	testutil.S(":5", "PFCOUNT", "h", "h2"),
	testutil.S(":5", "PFCOUNT", "h", "h2", "missing"),
	testutil.S(":0", "PFCOUNT", "missing"),
	testutil.S(":1", "PFADD", "empty"),
	testutil.S(":0", "PFADD", "empty"),
	testutil.S(":0", "PFCOUNT", "empty"),
	testutil.S("+OK", "PFMERGE", "m", "h", "h2"),
	testutil.S(":5", "PFCOUNT", "m"),
	testutil.S("+OK", "PFMERGE", "m"),
	testutil.S(":5", "PFCOUNT", "m"),
	testutil.S("+OK", "PFMERGE", "m2", "missing"),
	testutil.S(":0", "PFCOUNT", "m2"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(":1", "LPUSH", "lst", "a"),
	testutil.S("-WRONGTYPE Key is not a valid HyperLogLog string value.", "PFCOUNT", "str"),
	testutil.S("-WRONGTYPE Key is not a valid HyperLogLog string value.", "PFADD", "str", "a"),
	testutil.S("-WRONGTYPE Key is not a valid HyperLogLog string value.", "PFMERGE", "m", "str"),
	testutil.S(wrongType, "PFCOUNT", "lst"),
	testutil.S(wrongType, "PFADD", "lst", "a"),
	testutil.S(arityErr("pfcount"), "PFCOUNT"),
	testutil.S(arityErr("pfadd"), "PFADD"),
	testutil.S(arityErr("pfmerge"), "PFMERGE"),
}

const (
	palermoLon = "13.361389"
	palermoLat = "38.115556"
	cataniaLon = "15.087269"
	cataniaLat = "37.502669"
	// Coordinates as Redis returns them after geohash encoding.
	palermoPos = `*["13.36138933897018433", "38.11555639549629859"]`
	cataniaPos = `*["15.08726745843887329", "37.50266842333162032"]`
)

// hllDenseScript promotes an HLL to the dense encoding. Its estimates are
// compared exactly against Redis (same hash and estimator) in the
// differential run; TestHllGeoCommands_DenseEstimate checks them within the
// HLL error bound.
var hllDenseScript = []testutil.Step{
	testutil.S(":1", "PFADD", "big", "x"),
	testutil.S(":1", hllBulkAdd("big", 5000)...),
	testutil.S(`"\x00"`, "GETRANGE", "big", "4", "4"),
	testutil.S("", "PFCOUNT", "big"),
	testutil.S("+OK", "PFMERGE", "bm", "big", "h"),
	testutil.S("", "PFCOUNT", "bm"),
	testutil.S("", "PFCOUNT", "big", "h"),
	testutil.S(":0", hllBulkAdd("big", 5000)...),
}

// geoUnverifiedScript runs only in the differential test: Redis 8.4's exact
// error texts for these steps are not verified here.
var geoUnverifiedScript = []testutil.Step{
	testutil.S("", "ZRANGE", "dst3", "0", "-1", "WITHSCORES"),
	testutil.S("", "GEOADD", "gu", palermoLon, palermoLat, "Palermo"),
	testutil.S("", "GEOSEARCHSTORE", "dst", "gu", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "WITHDIST"),
	testutil.S("", "GEORADIUS", "gu", "15", "37", "200", "km", "WITHDIST", "STORE", "dst"),
	testutil.S("", "GEOSEARCH", "gu", "FROMLONLAT", "15", "37"),
	testutil.S("", "GEOSEARCH", "gu", "BYRADIUS", "200", "km"),
	testutil.S("", "GEOSEARCH", "gu", "FROMMEMBER", "Palermo", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"),
}

// geoScript covers every geo command.
var geoScript = []testutil.Step{
	testutil.S(":2", "GEOADD", "g", palermoLon, palermoLat, "Palermo", cataniaLon, cataniaLat, "Catania"),
	testutil.S(":0", "GEOADD", "g", "NX", "13.5", "38.1", "Palermo"),
	testutil.S(":0", "GEOADD", "g", "XX", "13.5", "38.1", "Nowhere"),
	testutil.S(":0", "ZCARD", "nowhere"),
	testutil.S(":1", "GEOADD", "g", "XX", "CH", "13.5", "38.1", "Palermo"),
	testutil.S(":1", "GEOADD", "g", "CH", palermoLon, palermoLat, "Palermo"),
	testutil.S(":0", "GEOADD", "g", "CH", palermoLon, palermoLat, "Palermo"),
	testutil.S("-ERR invalid longitude,latitude pair 200.000000,38.000000", "GEOADD", "g", "200", "38", "x"),
	testutil.S("-ERR invalid longitude,latitude pair 10.000000,86.000000", "GEOADD", "g", "10", "86", "x"),
	testutil.S(errNotFloat, "GEOADD", "g", "x", "38", "x"),
	testutil.S(`"3479099956230698"`, "ZSCORE", "g", "Palermo"),
	testutil.S(`"3479447370796909"`, "ZSCORE", "g", "Catania"),
	testutil.S("*["+palermoPos+", "+cataniaPos+", (nil-array)]", "GEOPOS", "g", "Palermo", "Catania", "nope"),
	testutil.S("*[(nil-array)]", "GEOPOS", "missing", "a"),
	testutil.S(`"166274.1516"`, "GEODIST", "g", "Palermo", "Catania"),
	testutil.S(`"166274.1516"`, "GEODIST", "g", "Palermo", "Catania", "m"),
	testutil.S(`"166.2742"`, "GEODIST", "g", "Palermo", "Catania", "km"),
	testutil.S(`"103.3182"`, "GEODIST", "g", "Palermo", "Catania", "MI"),
	testutil.S(`"545518.8700"`, "GEODIST", "g", "Palermo", "Catania", "ft"),
	testutil.S(`"0.0000"`, "GEODIST", "g", "Palermo", "Palermo"),
	testutil.S("(nil)", "GEODIST", "g", "Palermo", "nope"),
	testutil.S("(nil)", "GEODIST", "missing", "a", "b"),
	testutil.S("-ERR unsupported unit provided. please use M, KM, FT, MI", "GEODIST", "g", "Palermo", "Catania", "yd"),
	testutil.S(`*["sqc8b49rny0", "sqdtr74hyu0", (nil)]`, "GEOHASH", "g", "Palermo", "Catania", "nope"),
	testutil.S("*[(nil)]", "GEOHASH", "missing", "a"),
	testutil.S(`*["Catania", "Palermo"]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC"),
	testutil.S(`*["Palermo", "Catania"]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "DESC"),
	testutil.S(`*["Catania"]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "100", "km"),
	testutil.S(`*[*["Catania", "56.4413"], *["Palermo", "190.4424"]]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "WITHDIST"),
	testutil.S(`*[*["Catania", "56.4413", `+cataniaPos+`], *["Palermo", "190.4424", `+palermoPos+`]]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYBOX", "400", "400", "km", "ASC", "WITHCOORD", "WITHDIST"),
	testutil.S(`*[*["Catania", `+cataniaPos+`]]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "COUNT", "1", "WITHCOORD"),
	testutil.S(`*["Catania", "Palermo"]`, "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "COUNT", "2", "ANY"),
	testutil.S(`*[*["Palermo", "0.0000"], *["Catania", "166.2742"]]`, "GEOSEARCH", "g", "FROMMEMBER", "Palermo", "BYRADIUS", "200", "km", "ASC", "WITHDIST"),
	testutil.S(`*["Palermo"]`, "GEOSEARCH", "g", "FROMMEMBER", "Palermo", "BYBOX", "10", "10", "km"),
	testutil.S("*[]", "GEOSEARCH", "missing", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"),
	testutil.S("-ERR could not decode requested zset member", "GEOSEARCH", "g", "FROMMEMBER", "nope", "BYRADIUS", "200", "km"),
	testutil.S("-ERR the ANY argument requires COUNT argument", "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ANY"),
	testutil.S("-ERR COUNT must be > 0", "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "COUNT", "0"),
	testutil.S("-ERR unsupported unit provided. please use M, KM, FT, MI", "GEOSEARCH", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "yd"),
	testutil.S(":2", "GEOSEARCHSTORE", "dst", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"),
	testutil.S(`*["Palermo", "3479099956230698", "Catania", "3479447370796909"]`, "ZRANGE", "dst", "0", "-1", "WITHSCORES"),
	testutil.S(":1", "GEOSEARCHSTORE", "dst", "g", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km", "ASC", "COUNT", "1", "STOREDIST"),
	testutil.S(`*["Catania"]`, "ZRANGE", "dst", "0", "-1"),
	testutil.S(":0", "GEOSEARCHSTORE", "dst", "g", "FROMLONLAT", "0", "0", "BYRADIUS", "1", "km"),
	testutil.S(":0", "EXISTS", "dst"),
	testutil.S(`*[*["Catania", "56.4413"], *["Palermo", "190.4424"]]`, "GEORADIUS", "g", "15", "37", "200", "km", "WITHDIST", "ASC"),
	testutil.S(`*["Catania"]`, "GEORADIUS_RO", "g", "15", "37", "100", "km"),
	testutil.S(`*["Palermo", "Catania"]`, "GEORADIUSBYMEMBER", "g", "Palermo", "200", "km", "ASC"),
	testutil.S(`*[*["Palermo", "0.0000"]]`, "GEORADIUSBYMEMBER_RO", "g", "Palermo", "10", "km", "WITHDIST"),
	testutil.S(":2", "GEORADIUS", "g", "15", "37", "200", "km", "STORE", "dst2"),
	testutil.S(":2", "ZCARD", "dst2"),
	testutil.S(":1", "GEORADIUSBYMEMBER", "g", "Palermo", "10", "km", "STOREDIST", "dst3"),
	testutil.S(`*["Palermo"]`, "ZRANGE", "dst3", "0", "-1"),
	testutil.S("-ERR could not decode requested zset member", "GEORADIUSBYMEMBER", "g", "nope", "10", "km"),
	testutil.S("+OK", "SET", "str", "v"),
	testutil.S(wrongType, "GEOADD", "str", "1", "1", "x"),
	testutil.S(wrongType, "GEOPOS", "str", "x"),
	testutil.S(wrongType, "GEOSEARCH", "str", "FROMLONLAT", "15", "37", "BYRADIUS", "200", "km"),
	testutil.S(arityErr("geoadd"), "GEOADD", "g", "1", "1"),
	testutil.S(arityErr("geodist"), "GEODIST", "g", "a"),
}

func TestHllGeoCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, hllScript)
	testutil.RunScript(t, c, geoScript)
}

func TestHllGeoCommandsDifferential(t *testing.T) {
	testutil.Differential(t, concat(hllScript, hllDenseScript, geoScript, geoUnverifiedScript), testutil.KnownDivergences)
}

// Redis creates small HLLs in the sparse encoding (byte 4 of the header is 1)
// and only promotes them to dense past hll-sparse-max-bytes.
func TestHllGeoCommands_SparseEncoding(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(":1", "PFADD", "h", "a", "b", "c"),
		testutil.S(`"\x01"`, "GETRANGE", "h", "4", "4"),
		testutil.S(":1", hllBulkAdd("h", 5000)...),
		testutil.S(`"\x00"`, "GETRANGE", "h", "4", "4"),
	})
}

// TestHllGeoCommands_DenseEstimate checks the dense-HLL estimates of
// hllDenseScript against the true cardinalities within 3% (the standard
// error of a 16384-register HLL is 0.81%).
func TestHllGeoCommands_DenseEstimate(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, hllScript)
	exact := map[string]float64{"PFCOUNT big": 5001, "PFCOUNT bm": 5006, "PFCOUNT big h": 5006}
	for _, st := range hllDenseScript {
		if st.Want != "" {
			testutil.RunScript(t, c, []testutil.Step{st})
			continue
		}
		testutil.RecordCommands(t)(st.Cmd[0])
		v, err := c.Do(st.Cmd...)
		if err != nil {
			t.Fatalf("%v: %v", st.Cmd, err)
		}
		want := exact[strings.Join(st.Cmd, " ")]
		if got := float64(v.Int); math.Abs(got-want)/want > 0.03 {
			t.Fatalf("%v: got %v, want within 3%% of %v", st.Cmd, got, want)
		}
	}
}

// Redis accepts a sparse header and only detects the corruption while
// summing the registers in PFCOUNT.
func TestHllGeoCommands_CorruptSparseHLL(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("+OK", "SET", "bad", corruptSparseHLL),
		testutil.S("-INVALIDOBJ Corrupted HLL object detected", "PFCOUNT", "bad"),
	})
}

// Redis's geoaddCommand folds XX+NX into its argument-count syntax check.
func TestHllGeoCommands_GeoAddXXNX(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(errSyntax, "GEOADD", "g", "XX", "NX", "1", "1", "x"),
		testutil.S(errSyntax, "GEOADD", "g", "1", "2", "m", "3"),
	})
}
