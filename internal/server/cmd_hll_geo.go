package server

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

const errNotHLL = "WRONGTYPE Key is not a valid HyperLogLog string value."

// ---- HyperLogLog ----

func (c *Ctx) getHLL(key string) (*ds.HLL, []byte, bool) {
	v, e, ok := c.getStr(key)
	if !ok {
		return nil, nil, false
	}
	if e == nil {
		return nil, nil, true
	}
	h, valid := ds.DecodeHLL(v)
	if !valid {
		c.Err(errNotHLL)
		return nil, nil, false
	}
	return h, v, true
}

func cmdPFAdd(c *Ctx) {
	key := c.Arg(1)
	h, _, ok := c.getHLL(key)
	if !ok {
		return
	}
	created := false
	if h == nil {
		h = &ds.HLL{}
		created = true
	}
	updated := false
	for _, el := range c.Args[2:] {
		if h.Add(el) {
			updated = true
		}
	}
	if created || updated {
		c.DB.SetValue(key, h.Encode(), true)
		c.Dirty(1)
		c.W.WriteInt(1)
		return
	}
	c.W.WriteInt(0)
}

func cmdPFCount(c *Ctx) {
	if len(c.Args) == 2 {
		h, _, ok := c.getHLL(c.Arg(1))
		if !ok {
			return
		}
		if h == nil {
			c.W.WriteInt(0)
			return
		}
		c.W.WriteInt(int64(h.Count()))
		return
	}
	acc := &ds.HLL{}
	for _, k := range c.Args[1:] {
		h, _, ok := c.getHLL(string(k))
		if !ok {
			return
		}
		if h != nil {
			acc.Merge(h)
		}
	}
	c.W.WriteInt(int64(acc.Count()))
}

func cmdPFMerge(c *Ctx) {
	acc := &ds.HLL{}
	for _, k := range c.Args[1:] {
		h, _, ok := c.getHLL(string(k))
		if !ok {
			return
		}
		if h != nil {
			acc.Merge(h)
		}
	}
	c.DB.SetValue(c.Arg(1), acc.Encode(), true)
	c.Dirty(1)
	c.W.WriteOK()
}

// ---- GEO ----

func unitFactor(u string) (float64, bool) {
	switch strings.ToLower(u) {
	case "m":
		return 1, true
	case "km":
		return 1000, true
	case "ft":
		return 0.3048, true
	case "mi":
		return 1609.34, true
	}
	return 0, false
}

func formatHuman(f float64) string {
	s := strconv.FormatFloat(f, 'f', 17, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" {
		s = "0"
	}
	return s
}

func writeHumanDouble(w *resp.Writer, f float64) {
	if w.Proto >= 3 {
		w.WriteRaw([]byte("," + formatHuman(f) + "\r\n"))
		return
	}
	w.WriteBulkString(formatHuman(f))
}

func writeDistance(w *resp.Writer, d float64) {
	s := strconv.FormatFloat(d, 'f', 4, 64)
	if w.Proto >= 3 {
		w.WriteRaw([]byte("," + s + "\r\n"))
		return
	}
	w.WriteBulkString(s)
}

func cmdGeoAdd(c *Ctx) {
	key := c.Arg(1)
	i := 2
	var nx, xx, ch bool
	for ; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "nx"):
			nx = true
		case c.Is(i, "xx"):
			xx = true
		case c.Is(i, "ch"):
			ch = true
		default:
			goto done
		}
	}
done:
	rest := c.Args[i:]
	if len(rest) == 0 || len(rest)%3 != 0 {
		c.Err("ERR syntax error. Try GEOADD key [x1] [y1] [name1] [x2] [y2] [name2] ... ")
		return
	}
	if nx && xx {
		c.Err("ERR XX and NX options at the same time are not compatible")
		return
	}
	type item struct {
		score  float64
		member string
	}
	items := make([]item, 0, len(rest)/3)
	for j := 0; j < len(rest); j += 3 {
		lon, ok1 := parseFloat(rest[j])
		lat, ok2 := parseFloat(rest[j+1])
		if !ok1 || !ok2 {
			c.Err(errNotFloat)
			return
		}
		bits, ok := ds.GeoEncode(lon, lat)
		if !ok {
			c.Err(fmt.Sprintf("ERR invalid longitude,latitude pair %f,%f", lon, lat))
			return
		}
		items = append(items, item{float64(bits), string(rest[j+2])})
	}
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil {
		if xx {
			c.W.WriteInt(0)
			return
		}
		z = ds.NewZSet()
		c.DB.Add(key, z)
	}
	var added, changed int64
	for _, it := range items {
		cur, exists := z.Score(it.member)
		if exists {
			if nx {
				continue
			}
			if cur != it.score {
				z.Add(it.member, it.score)
				changed++
			}
		} else {
			if xx {
				continue
			}
			z.Add(it.member, it.score)
			added++
		}
	}
	if z.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(added + changed)
	if ch {
		c.W.WriteInt(added + changed)
	} else {
		c.W.WriteInt(added)
	}
}

func cmdGeoPos(c *Ctx) {
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(c.Args) - 2)
	for _, m := range c.Args[2:] {
		if z == nil {
			c.W.WriteNullArray()
			continue
		}
		s, exists := z.Score(string(m))
		if !exists {
			c.W.WriteNullArray()
			continue
		}
		lon, lat := ds.GeoDecode(uint64(s))
		c.W.WriteArrayLen(2)
		writeHumanDouble(c.W, lon)
		writeHumanDouble(c.W, lat)
	}
}

func cmdGeoDist(c *Ctx) {
	factor := 1.0
	if len(c.Args) == 5 {
		f, ok := unitFactor(c.Arg(4))
		if !ok {
			c.Err("ERR unsupported unit provided. please use M, KM, FT, MI")
			return
		}
		factor = f
	} else if len(c.Args) > 5 {
		c.SyntaxErr()
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteNull()
		return
	}
	s1, ok1 := z.Score(c.Arg(2))
	s2, ok2 := z.Score(c.Arg(3))
	if !ok1 || !ok2 {
		c.W.WriteNull()
		return
	}
	lon1, lat1 := ds.GeoDecode(uint64(s1))
	lon2, lat2 := ds.GeoDecode(uint64(s2))
	writeDistance(c.W, ds.GeoDistance(lon1, lat1, lon2, lat2)/factor)
}

func cmdGeoHash(c *Ctx) {
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(c.Args) - 2)
	for _, m := range c.Args[2:] {
		if z == nil {
			c.W.WriteNull()
			continue
		}
		s, exists := z.Score(string(m))
		if !exists {
			c.W.WriteNull()
			continue
		}
		c.W.WriteBulkString(ds.GeoHashString(uint64(s)))
	}
}

type geoQuery struct {
	fromMember   string
	hasMember    bool
	lon, lat     float64
	hasLonLat    bool
	radius       float64
	byRadius     bool
	width        float64
	height       float64
	byBox        bool
	factor       float64
	sort         int // 0 none, 1 asc, 2 desc
	count        int64
	any          bool
	withCoord    bool
	withDist     bool
	withHash     bool
	store        string
	storeDist    bool
	hasStore     bool
	readonly     bool
	searchSyntax bool
}

type geoPoint struct {
	member string
	dist   float64
	score  float64
	lon    float64
	lat    float64
}

// parseGeoOpts parses trailing GEORADIUS/GEOSEARCH options from i.
func (c *Ctx) parseGeoOpts(q *geoQuery, i int, storeAllowed bool) bool {
	for ; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case c.Is(i, "withdist"):
			q.withDist = true
		case c.Is(i, "withhash"):
			q.withHash = true
		case c.Is(i, "withcoord"):
			q.withCoord = true
		case c.Is(i, "any"):
			q.any = true
		case c.Is(i, "asc"):
			q.sort = 1
		case c.Is(i, "desc"):
			q.sort = 2
		case c.Is(i, "count") && left >= 1:
			n, ok := c.Int(i + 1)
			if !ok {
				return false
			}
			if n <= 0 {
				c.Err("ERR COUNT must be > 0")
				return false
			}
			q.count = n
			i++
		case storeAllowed && !q.searchSyntax && (c.Is(i, "store") || c.Is(i, "storedist")) && left >= 1:
			q.store = c.Arg(i + 1)
			q.hasStore = true
			q.storeDist = c.Is(i, "storedist")
			i++
		case storeAllowed && q.searchSyntax && c.Is(i, "storedist"):
			q.storeDist = true
		case q.searchSyntax && c.Is(i, "frommember") && left >= 1:
			q.fromMember = c.Arg(i + 1)
			if q.hasMember || q.hasLonLat {
				c.Err("ERR exactly one of FROMMEMBER or FROMLONLAT can be specified for " + strings.ToUpper(c.Cmd.Name))
				return false
			}
			q.hasMember = true
			i++
		case q.searchSyntax && c.Is(i, "fromlonlat") && left >= 2:
			if q.hasMember || q.hasLonLat {
				c.Err("ERR exactly one of FROMMEMBER or FROMLONLAT can be specified for " + strings.ToUpper(c.Cmd.Name))
				return false
			}
			lon, ok1 := parseFloat(c.Args[i+1])
			lat, ok2 := parseFloat(c.Args[i+2])
			if !ok1 || !ok2 {
				c.Err(errNotFloat)
				return false
			}
			if _, ok := ds.GeoEncode(lon, lat); !ok {
				c.Err(fmt.Sprintf("ERR invalid longitude,latitude pair %f,%f", lon, lat))
				return false
			}
			q.lon, q.lat, q.hasLonLat = lon, lat, true
			i += 2
		case q.searchSyntax && c.Is(i, "byradius") && left >= 2:
			if q.byRadius || q.byBox {
				c.Err("ERR exactly one of BYRADIUS and BYBOX can be specified for " + strings.ToUpper(c.Cmd.Name))
				return false
			}
			r, ok := parseFloat(c.Args[i+1])
			if !ok {
				c.Err("ERR need numeric radius")
				return false
			}
			if r < 0 {
				c.Err("ERR radius cannot be negative")
				return false
			}
			f, ok := unitFactor(c.Arg(i + 2))
			if !ok {
				c.Err("ERR unsupported unit provided. please use M, KM, FT, MI")
				return false
			}
			q.radius, q.factor, q.byRadius = r*f, f, true
			i += 2
		case q.searchSyntax && c.Is(i, "bybox") && left >= 3:
			if q.byRadius || q.byBox {
				c.Err("ERR exactly one of BYRADIUS and BYBOX can be specified for " + strings.ToUpper(c.Cmd.Name))
				return false
			}
			w, ok1 := parseFloat(c.Args[i+1])
			h, ok2 := parseFloat(c.Args[i+2])
			if !ok1 || !ok2 {
				c.Err("ERR need numeric width and height")
				return false
			}
			if w < 0 || h < 0 {
				c.Err("ERR height or width cannot be negative")
				return false
			}
			f, ok := unitFactor(c.Arg(i + 3))
			if !ok {
				c.Err("ERR unsupported unit provided. please use M, KM, FT, MI")
				return false
			}
			q.width, q.height, q.factor, q.byBox = w*f, h*f, f, true
			i += 3
		default:
			c.SyntaxErr()
			return false
		}
	}
	if q.searchSyntax {
		if !q.hasMember && !q.hasLonLat {
			c.Err("ERR exactly one of FROMMEMBER or FROMLONLAT can be specified for " + strings.ToUpper(c.Cmd.Name))
			return false
		}
		if !q.byRadius && !q.byBox {
			c.Err("ERR exactly one of BYRADIUS and BYBOX can be specified for " + strings.ToUpper(c.Cmd.Name))
			return false
		}
	}
	if q.any && q.count == 0 {
		c.Err("ERR the ANY argument requires COUNT argument")
		return false
	}
	if (q.hasStore || (q.searchSyntax && q.store != "")) && (q.withDist || q.withHash || q.withCoord) {
		if q.searchSyntax {
			c.Err("ERR WITHDIST, WITHHASH and WITHCOORD options are not compatible with GEOSEARCHSTORE")
		} else {
			c.Err("ERR STORE option in GEORADIUS is not compatible with WITHDIST, WITHHASH and WITHCOORD options")
		}
		return false
	}
	return true
}

func (c *Ctx) geoExecute(q *geoQuery, srcKey string) {
	z, ok := c.getZSet(srcKey)
	if !ok {
		return
	}
	if q.hasMember {
		if z == nil {
			c.Err("ERR could not decode requested zset member")
			return
		}
		s, exists := z.Score(q.fromMember)
		if !exists {
			c.Err("ERR could not decode requested zset member")
			return
		}
		q.lon, q.lat = ds.GeoDecode(uint64(s))
	}
	var pts []geoPoint
	if z != nil {
		z.ForEach(func(m string, s float64) bool {
			lon, lat := ds.GeoDecode(uint64(s))
			var d float64
			if q.byBox {
				dist, in := ds.GeoInRectangle(q.width, q.height, q.lon, q.lat, lon, lat)
				if !in {
					return true
				}
				d = dist
			} else {
				d = ds.GeoDistance(q.lon, q.lat, lon, lat)
				if d > q.radius {
					return true
				}
			}
			pts = append(pts, geoPoint{member: m, dist: d, score: s, lon: lon, lat: lat})
			return !(q.any && int64(len(pts)) >= q.count)
		})
	}
	if q.sort == 0 && q.count > 0 && !q.any {
		q.sort = 1
	}
	if q.sort == 1 {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].dist < pts[j].dist })
	} else if q.sort == 2 {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].dist > pts[j].dist })
	}
	if q.count > 0 && int64(len(pts)) > q.count {
		pts = pts[:q.count]
	}
	if q.store != "" {
		if len(pts) == 0 {
			if c.DB.Delete(q.store) {
				c.Dirty(1)
			}
			c.W.WriteInt(0)
			return
		}
		nz := ds.NewZSet()
		for _, p := range pts {
			if q.storeDist {
				nz.Add(p.member, p.dist/q.factor)
			} else {
				nz.Add(p.member, p.score)
			}
		}
		c.DB.SetValue(q.store, nz, false)
		c.Dirty(1)
		c.W.WriteInt(int64(len(pts)))
		return
	}
	extra := 0
	if q.withDist {
		extra++
	}
	if q.withHash {
		extra++
	}
	if q.withCoord {
		extra++
	}
	c.W.WriteArrayLen(len(pts))
	for _, p := range pts {
		if extra == 0 {
			c.W.WriteBulkString(p.member)
			continue
		}
		c.W.WriteArrayLen(extra + 1)
		c.W.WriteBulkString(p.member)
		if q.withDist {
			writeDistance(c.W, p.dist/q.factor)
		}
		if q.withHash {
			c.W.WriteInt(int64(p.score))
		}
		if q.withCoord {
			c.W.WriteArrayLen(2)
			writeHumanDouble(c.W, p.lon)
			writeHumanDouble(c.W, p.lat)
		}
	}
}

func georadiusGeneric(c *Ctx, byMember, ro bool) {
	q := &geoQuery{readonly: ro, byRadius: true}
	i := 2
	if byMember {
		q.fromMember = c.Arg(2)
		q.hasMember = true
		i = 3
	} else {
		lon, ok1 := parseFloat(c.Args[2])
		lat, ok2 := parseFloat(c.Args[3])
		if !ok1 || !ok2 {
			c.Err(errNotFloat)
			return
		}
		if _, ok := ds.GeoEncode(lon, lat); !ok {
			c.Err(fmt.Sprintf("ERR invalid longitude,latitude pair %f,%f", lon, lat))
			return
		}
		q.lon, q.lat, q.hasLonLat = lon, lat, true
		i = 4
	}
	r, ok := parseFloat(c.Args[i])
	if !ok {
		c.Err("ERR need numeric radius")
		return
	}
	if r < 0 {
		c.Err("ERR radius cannot be negative")
		return
	}
	f, ok := unitFactor(c.Arg(i + 1))
	if !ok {
		c.Err("ERR unsupported unit provided. please use M, KM, FT, MI")
		return
	}
	q.radius, q.factor = r*f, f
	if !c.parseGeoOpts(q, i+2, !ro) {
		return
	}
	c.geoExecute(q, c.Arg(1))
}

func cmdGeoRadius(c *Ctx)           { georadiusGeneric(c, false, false) }
func cmdGeoRadiusRO(c *Ctx)         { georadiusGeneric(c, false, true) }
func cmdGeoRadiusByMember(c *Ctx)   { georadiusGeneric(c, true, false) }
func cmdGeoRadiusByMemberRO(c *Ctx) { georadiusGeneric(c, true, true) }

func cmdGeoSearch(c *Ctx) {
	q := &geoQuery{searchSyntax: true}
	if !c.parseGeoOpts(q, 2, false) {
		return
	}
	c.geoExecute(q, c.Arg(1))
}

func cmdGeoSearchStore(c *Ctx) {
	q := &geoQuery{searchSyntax: true, store: c.Arg(1)}
	if !c.parseGeoOpts(q, 3, true) {
		return
	}
	c.geoExecute(q, c.Arg(2))
}

func georadiusKeys(args [][]byte) ([]int, bool) {
	pos := []int{1}
	for i := 2; i < len(args)-1; i++ {
		a := strings.ToLower(string(args[i]))
		if a == "store" || a == "storedist" {
			pos = append(pos, i+1)
			i++
		}
	}
	return pos, false
}

var _ = math.Inf
