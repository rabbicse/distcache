package ds

import (
	"math"
	"testing"
)

var cities = []struct {
	name     string
	lon, lat float64
}{
	{"Paris", 2.3522, 48.8566},
	{"Berlin", 13.4050, 52.5200},
	{"New York", -74.0060, 40.7128},
	{"Tokyo", 139.6917, 35.6895},
	{"Sydney", 151.2093, -33.8688},
	{"Sao Paulo", -46.6333, -23.5505},
	{"Cairo", 31.2357, 30.0444},
	{"Reykjavik", -21.9426, 64.1466},
	{"Palermo", 13.361389, 38.115556},
	{"Wellington", 174.7762, -41.2865},
}

func TestGeo_EncodeDecode_RoundTripWithin1m(t *testing.T) {
	for _, c := range cities {
		t.Run(c.name, func(t *testing.T) {
			bits, ok := GeoEncode(c.lon, c.lat)
			if !ok {
				t.Fatal("encode failed")
			}
			if bits >= 1<<52 {
				t.Errorf("score %d exceeds 52 bits", bits)
			}
			lon, lat := GeoDecode(bits)
			if d := GeoDistance(c.lon, c.lat, lon, lat); d > 1 {
				t.Errorf("round trip drifted %.3f m", d)
			}
		})
	}
}

func TestGeo_Encode_OutOfRange(t *testing.T) {
	tests := []struct {
		name     string
		lon, lat float64
	}{
		{"lon high", 180.1, 0},
		{"lon low", -181, 0},
		{"lat high", 0, 85.06},
		{"lat low", 0, -85.06},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := GeoEncode(tc.lon, tc.lat); ok {
				t.Error("out-of-range coordinate accepted")
			}
		})
	}
}

func TestGeo_Encode_PalermoMatchesRedis(t *testing.T) {
	// Values from the Redis GEOADD / GEOHASH documentation.
	bits, _ := GeoEncode(13.361389, 38.115556)
	if bits != 3479099956230698 {
		t.Errorf("score got %d, want 3479099956230698", bits)
	}
	if got := GeoHashString(bits); got != "sqc8b49rny0" {
		t.Errorf("geohash got %s, want sqc8b49rny0", got)
	}
	bits, _ = GeoEncode(15.087269, 37.502669)
	if got := GeoHashString(bits); got != "sqdtr74hyu0" {
		t.Errorf("Catania geohash got %s, want sqdtr74hyu0", got)
	}
}

func TestGeo_Distance(t *testing.T) {
	tests := []struct {
		name       string
		a, b       int
		wantMeters float64
		tolerance  float64
	}{
		{"Paris-Berlin", 0, 1, 878000, 0.01},
		{"Paris-New York", 0, 2, 5837000, 0.01},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := cities[tc.a], cities[tc.b]
			got := GeoDistance(a.lon, a.lat, b.lon, b.lat)
			if math.Abs(got-tc.wantMeters)/tc.wantMeters > tc.tolerance {
				t.Errorf("got %.0f m, want %.0f m +-%.0f%%", got, tc.wantMeters, tc.tolerance*100)
			}
			rev := GeoDistance(b.lon, b.lat, a.lon, a.lat)
			if math.Abs(got-rev) > 1e-6 {
				t.Errorf("not symmetric: %f vs %f", got, rev)
			}
		})
	}
	// Palermo-Catania from the Redis docs: 166274.1516 m.
	if got := GeoDistance(13.361389, 38.115556, 15.087269, 37.502669); math.Abs(got-166274.1516) > 1 {
		t.Errorf("Palermo-Catania got %f, want 166274.15", got)
	}
	if d := GeoDistance(10, 20, 10, 20); d > 1e-6 {
		t.Errorf("same point got %f, want 0", d)
	}
	// Same longitude takes the latitude-only shortcut.
	if got, want := GeoDistance(5, 10, 5, 11), GeoLatDistance(10, 11); math.Abs(got-want) > 1e-6 { // FMA fusion on arm64 differs in the last ulps
		t.Errorf("got %f, want %f", got, want)
	}
}

func TestGeo_InRectangle(t *testing.T) {
	d, ok := GeoInRectangle(2_000_000, 2_000_000, 0, 0, 1, 1)
	if !ok || d <= 0 {
		t.Errorf("inside got %f,%v", d, ok)
	}
	if _, ok := GeoInRectangle(1000, 2_000_000, 0, 0, 1, 1); ok {
		t.Error("point outside width accepted")
	}
	if _, ok := GeoInRectangle(2_000_000, 1000, 0, 0, 1, 1); ok {
		t.Error("point outside height accepted")
	}
}

func TestGeo_InterleaveRoundTrip(t *testing.T) {
	for _, p := range [][2]uint32{{0, 0}, {1, 2}, {0xffffffff, 0}, {0x12345678, 0x9abcdef0}} {
		x, y := p[0], p[1]
		sep := deinterleave64(interleave64(x, y))
		if uint32(sep) != x || uint32(sep>>32) != y {
			t.Errorf("(%x,%x) got %x", x, y, sep)
		}
	}
}
