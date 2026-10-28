package ds

import "math"

// Geohash helpers ported from Redis geohash.c / geohash_helper.c so GEO
// commands produce identical scores, positions and distances.

const (
	GeoStepMax  = 26
	GeoLatMin   = -85.05112878
	GeoLatMax   = 85.05112878
	GeoLongMin  = -180.0
	GeoLongMax  = 180.0
	earthRadius = 6372797.560856
)

func interleave64(xlo, ylo uint32) uint64 {
	B := []uint64{0x5555555555555555, 0x3333333333333333, 0x0F0F0F0F0F0F0F0F, 0x00FF00FF00FF00FF, 0x0000FFFF0000FFFF}
	S := []uint{1, 2, 4, 8, 16}
	x, y := uint64(xlo), uint64(ylo)
	x = (x | (x << S[4])) & B[4]
	y = (y | (y << S[4])) & B[4]
	x = (x | (x << S[3])) & B[3]
	y = (y | (y << S[3])) & B[3]
	x = (x | (x << S[2])) & B[2]
	y = (y | (y << S[2])) & B[2]
	x = (x | (x << S[1])) & B[1]
	y = (y | (y << S[1])) & B[1]
	x = (x | (x << S[0])) & B[0]
	y = (y | (y << S[0])) & B[0]
	return x | (y << 1)
}

func deinterleave64(in uint64) uint64 {
	B := []uint64{0x5555555555555555, 0x3333333333333333, 0x0F0F0F0F0F0F0F0F, 0x00FF00FF00FF00FF, 0x0000FFFF0000FFFF, 0x00000000FFFFFFFF}
	S := []uint{0, 1, 2, 4, 8, 16}
	x := in
	y := in >> 1
	x = (x | (x >> S[0])) & B[0]
	y = (y | (y >> S[0])) & B[0]
	x = (x | (x >> S[1])) & B[1]
	y = (y | (y >> S[1])) & B[1]
	x = (x | (x >> S[2])) & B[2]
	y = (y | (y >> S[2])) & B[2]
	x = (x | (x >> S[3])) & B[3]
	y = (y | (y >> S[3])) & B[3]
	x = (x | (x >> S[4])) & B[4]
	y = (y | (y >> S[4])) & B[4]
	x = (x | (x >> S[5])) & B[5]
	y = (y | (y >> S[5])) & B[5]
	return x | (y << 32)
}

func geoEncode(lonMin, lonMax, latMin, latMax, lon, lat float64, step uint) (uint64, bool) {
	if lon > GeoLongMax || lon < GeoLongMin || lat > GeoLatMax || lat < GeoLatMin {
		return 0, false
	}
	if lat < latMin || lat > latMax || lon < lonMin || lon > lonMax {
		return 0, false
	}
	latOff := (lat - latMin) / (latMax - latMin)
	lonOff := (lon - lonMin) / (lonMax - lonMin)
	latOff *= float64(uint64(1) << step)
	lonOff *= float64(uint64(1) << step)
	return interleave64(uint32(latOff), uint32(lonOff)), true
}

// GeoEncode returns the 52-bit score for a coordinate; ok=false when out of
// the valid range.
func GeoEncode(lon, lat float64) (uint64, bool) {
	return geoEncode(GeoLongMin, GeoLongMax, GeoLatMin, GeoLatMax, lon, lat, GeoStepMax)
}

// GeoDecode returns the cell centre for a score.
func GeoDecode(bits uint64) (lon, lat float64) {
	return geoDecodeRange(bits, GeoLongMin, GeoLongMax, GeoLatMin, GeoLatMax)
}

func geoDecodeRange(bits uint64, lonMin, lonMax, latMin, latMax float64) (float64, float64) {
	sep := deinterleave64(bits)
	latScale := latMax - latMin
	lonScale := lonMax - lonMin
	ilato := uint32(sep)
	ilono := uint32(sep >> 32)
	div := float64(uint64(1) << GeoStepMax)
	latMinA := latMin + (float64(ilato)*1.0/div)*latScale
	latMaxA := latMin + ((float64(ilato)+1)*1.0/div)*latScale
	lonMinA := lonMin + (float64(ilono)*1.0/div)*lonScale
	lonMaxA := lonMin + ((float64(ilono)+1)*1.0/div)*lonScale
	lon := (lonMinA + lonMaxA) / 2
	if lon > GeoLongMax {
		lon = GeoLongMax
	}
	if lon < GeoLongMin {
		lon = GeoLongMin
	}
	lat := (latMinA + latMaxA) / 2
	if lat > GeoLatMax {
		lat = GeoLatMax
	}
	if lat < GeoLatMin {
		lat = GeoLatMin
	}
	return lon, lat
}

func degRad(a float64) float64 { return a * (math.Pi / 180.0) }

// GeoLatDistance returns the distance between two latitudes in meters.
func GeoLatDistance(lat1, lat2 float64) float64 {
	return earthRadius * math.Abs(degRad(lat2)-degRad(lat1))
}

// GeoDistance computes the haversine distance in meters.
func GeoDistance(lon1, lat1, lon2, lat2 float64) float64 {
	lon1r := degRad(lon1)
	lon2r := degRad(lon2)
	v := math.Sin((lon2r - lon1r) / 2)
	if v == 0 {
		return GeoLatDistance(lat1, lat2)
	}
	lat1r := degRad(lat1)
	lat2r := degRad(lat2)
	u := math.Sin((lat2r - lat1r) / 2)
	a := u*u + math.Cos(lat1r)*math.Cos(lat2r)*v*v
	return 2.0 * earthRadius * math.Asin(math.Sqrt(a))
}

// GeoInRectangle mirrors geohashGetDistanceIfInRectangle.
func GeoInRectangle(widthM, heightM, x1, y1, x2, y2 float64) (float64, bool) {
	if GeoLatDistance(y2, y1) > heightM/2 {
		return 0, false
	}
	if GeoDistance(x2, y2, x1, y2) > widthM/2 {
		return 0, false
	}
	return GeoDistance(x1, y1, x2, y2), true
}

// GeoHashString returns the standard 11 character geohash for a score.
func GeoHashString(bits uint64) string {
	const alphabet = "0123456789bcdefghjkmnpqrstuvwxyz"
	lon, lat := GeoDecode(bits)
	h, ok := geoEncode(-180, 180, -90, 90, lon, lat, GeoStepMax)
	if !ok {
		// Out of the standard range can't happen for valid scores.
		h = 0
	}
	buf := make([]byte, 11)
	for i := 0; i < 11; i++ {
		idx := 0
		if i < 10 {
			idx = int((h >> (52 - uint((i+1)*5))) & 0x1f)
		}
		buf[i] = alphabet[idx]
	}
	return string(buf)
}
