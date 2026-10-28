package server

import (
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

// getHash returns the hash at key. Expired fields are purged when the
// context holds a write lock; a hash whose fields all expired is treated as
// missing.
func (c *Ctx) getHash(key string) (*ds.Hash, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, true
	}
	h, ok := e.Value.(*ds.Hash)
	if !ok {
		c.WrongType()
		return nil, false
	}
	if h.HasTTLs() {
		if c.DB.Write {
			if n := h.Purge(c.DB.Now); n > 0 && h.RawLen() == 0 {
				c.DB.Delete(key)
				return nil, true
			}
		} else if h.Len(c.DB.Now) == 0 {
			return nil, true
		}
	}
	return h, true
}

func (c *Ctx) hashOrCreate(key string) (*ds.Hash, bool) {
	h, ok := c.getHash(key)
	if !ok {
		return nil, false
	}
	if h == nil {
		h = ds.NewHash()
		c.DB.Add(key, h)
	}
	return h, true
}

func cmdHSet(c *Ctx) {
	if len(c.Args)%2 != 0 {
		c.WrongArgs()
		return
	}
	h, ok := c.hashOrCreate(c.Arg(1))
	if !ok {
		return
	}
	var added int64
	for i := 2; i < len(c.Args); i += 2 {
		if h.Set(c.Arg(i), c.Args[i+1], c.DB.Now) {
			added++
		}
	}
	c.Dirty(int64(len(c.Args)-2) / 2)
	if strings.EqualFold(c.Cmd.Name, "hmset") {
		c.W.WriteOK()
		return
	}
	c.W.WriteInt(added)
}

func cmdHSetNX(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h != nil {
		if _, exists := h.Get(c.Arg(2), c.DB.Now); exists {
			c.W.WriteInt(0)
			return
		}
	} else {
		h = ds.NewHash()
		c.DB.Add(c.Arg(1), h)
	}
	h.Set(c.Arg(2), c.Args[3], c.DB.Now)
	c.Dirty(1)
	c.W.WriteInt(1)
}

func cmdHGet(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h == nil {
		c.W.WriteNull()
		return
	}
	v, exists := h.Get(c.Arg(2), c.DB.Now)
	if !exists {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(v)
}

func cmdHMGet(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(c.Args) - 2)
	for _, f := range c.Args[2:] {
		if h == nil {
			c.W.WriteNull()
			continue
		}
		if v, exists := h.Get(string(f), c.DB.Now); exists {
			c.W.WriteBulk(v)
		} else {
			c.W.WriteNull()
		}
	}
}

func cmdHDel(c *Ctx) {
	key := c.Arg(1)
	h, ok := c.getHash(key)
	if !ok {
		return
	}
	if h == nil {
		c.W.WriteInt(0)
		return
	}
	var n int64
	for _, f := range c.Args[2:] {
		if h.Del(string(f), c.DB.Now) {
			n++
		}
	}
	if h.RawLen() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdHLen(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(h.Len(c.DB.Now)))
}

func cmdHStrlen(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h == nil {
		c.W.WriteInt(0)
		return
	}
	v, _ := h.Get(c.Arg(2), c.DB.Now)
	c.W.WriteInt(int64(len(v)))
}

func cmdHExists(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h == nil {
		c.W.WriteInt(0)
		return
	}
	_, exists := h.Get(c.Arg(2), c.DB.Now)
	if exists {
		c.W.WriteInt(1)
	} else {
		c.W.WriteInt(0)
	}
}

func hgetallGeneric(c *Ctx, keys, vals bool) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if h == nil {
		if keys && vals {
			c.W.WriteMapLen(0)
		} else {
			c.W.WriteArrayLen(0)
		}
		return
	}
	n := h.Len(c.DB.Now)
	if keys && vals {
		c.W.WriteMapLen(n)
	} else {
		c.W.WriteArrayLen(n)
	}
	h.ForEach(c.DB.Now, func(f string, v []byte) bool {
		if keys {
			c.W.WriteBulkString(f)
		}
		if vals {
			c.W.WriteBulk(v)
		}
		return true
	})
}

func cmdHGetAll(c *Ctx) { hgetallGeneric(c, true, true) }
func cmdHKeys(c *Ctx)   { hgetallGeneric(c, true, false) }
func cmdHVals(c *Ctx)   { hgetallGeneric(c, false, true) }

func cmdHIncrBy(c *Ctx) {
	incr, ok := c.Int(3)
	if !ok {
		return
	}
	h, ok := c.hashOrCreate(c.Arg(1))
	if !ok {
		return
	}
	var cur int64
	if v, exists := h.Get(c.Arg(2), c.DB.Now); exists {
		n, isInt := parseInt(v)
		if !isInt {
			c.Err("ERR hash value is not an integer")
			return
		}
		cur = n
	}
	if (incr < 0 && cur < 0 && incr < math.MinInt64-cur) || (incr > 0 && cur > 0 && incr > math.MaxInt64-cur) {
		c.Err(errOverflow)
		return
	}
	cur += incr
	h.SetKeepTTL(c.Arg(2), formatInt(cur))
	c.Dirty(1)
	c.W.WriteInt(cur)
}

func cmdHIncrByFloat(c *Ctx) {
	incr, ok := parseFloat(c.Args[3])
	if !ok || math.IsInf(incr, 0) {
		c.Err(errNotFloat)
		return
	}
	h, ok := c.hashOrCreate(c.Arg(1))
	if !ok {
		return
	}
	var cur float64
	if v, exists := h.Get(c.Arg(2), c.DB.Now); exists {
		f, isF := parseFloat(v)
		if !isF {
			c.Err("ERR hash value is not a float")
			return
		}
		cur = f
	}
	cur += incr
	if math.IsNaN(cur) || math.IsInf(cur, 0) {
		c.Err("ERR increment would produce NaN or Infinity")
		return
	}
	nb := []byte(formatLongDouble(cur))
	h.SetKeepTTL(c.Arg(2), nb)
	c.Dirty(1)
	c.Rewrite([]byte("HSET"), c.Args[1], c.Args[2], nb)
	c.W.WriteBulk(nb)
}

func cmdHRandField(c *Ctx) {
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	if len(c.Args) == 2 {
		if h == nil {
			c.W.WriteNull()
			return
		}
		var pick string
		h.ForEach(c.DB.Now, func(f string, _ []byte) bool { pick = f; return false })
		c.W.WriteBulkString(pick)
		return
	}
	count, ok := c.Int(2)
	if !ok {
		return
	}
	withValues := false
	if len(c.Args) == 4 {
		if !c.Is(3, "withvalues") {
			c.SyntaxErr()
			return
		}
		withValues = true
	} else if len(c.Args) > 4 {
		c.SyntaxErr()
		return
	}
	if count < -math.MaxInt64/2 || count > math.MaxInt64/2 {
		c.Err("ERR value is out of range")
		return
	}
	if h == nil || count == 0 {
		c.W.WriteArrayLen(0)
		return
	}
	var fields []string
	var vals [][]byte
	h.ForEach(c.DB.Now, func(f string, v []byte) bool {
		fields = append(fields, f)
		vals = append(vals, v)
		return true
	})
	var idx []int
	if count < 0 {
		for i := int64(0); i < -count; i++ {
			idx = append(idx, rand.IntN(len(fields)))
		}
	} else {
		perm := rand.Perm(len(fields))
		if count < int64(len(perm)) {
			perm = perm[:count]
		}
		idx = perm
	}
	writeRandPairs(c.W, idx, fields, vals, withValues)
}

func writeRandPairs(w *resp.Writer, idx []int, fields []string, vals [][]byte, withValues bool) {
	if withValues && w.Proto >= 3 {
		w.WriteArrayLen(len(idx))
		for _, i := range idx {
			w.WriteArrayLen(2)
			w.WriteBulkString(fields[i])
			w.WriteBulk(vals[i])
		}
		return
	}
	if withValues {
		w.WriteArrayLen(2 * len(idx))
	} else {
		w.WriteArrayLen(len(idx))
	}
	for _, i := range idx {
		w.WriteBulkString(fields[i])
		if withValues {
			w.WriteBulk(vals[i])
		}
	}
}

// parseScanOpts parses MATCH/COUNT/NOVALUES after the cursor.
func parseScanOpts(c *Ctx, from int, allowNoValues bool) (pattern string, noValues bool, ok bool) {
	for i := from; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "match") && i+1 < len(c.Args):
			pattern = c.Arg(i + 1)
			i++
		case c.Is(i, "count") && i+1 < len(c.Args):
			n, isInt := parseInt(c.Args[i+1])
			if !isInt {
				c.Err(errNotInt)
				return "", false, false
			}
			if n < 1 {
				c.SyntaxErr()
				return "", false, false
			}
			i++
		case allowNoValues && c.Is(i, "novalues"):
			noValues = true
		default:
			c.SyntaxErr()
			return "", false, false
		}
	}
	return pattern, noValues, true
}

func cmdHScan(c *Ctx) {
	if _, ok := parseCursor(c.Args[2]); !ok {
		c.Err("ERR invalid cursor")
		return
	}
	pattern, noValues, ok := parseScanOpts(c, 3, true)
	if !ok {
		return
	}
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(2)
	c.W.WriteBulkString("0")
	if h == nil {
		c.W.WriteArrayLen(0)
		return
	}
	var out [][]byte
	h.ForEach(c.DB.Now, func(f string, v []byte) bool {
		if pattern == "" || ds.Match(pattern, f, false) {
			out = append(out, []byte(f))
			if !noValues {
				out = append(out, v)
			}
		}
		return true
	})
	c.W.WriteBulks(out)
}

// ---- hash field expiration (Redis 7.4+) ----

// parseFields parses "FIELDS numfields f1 ... fn" at position i.
func parseFields(c *Ctx, i int) ([]string, bool) {
	if i >= len(c.Args) || !c.Is(i, "fields") {
		c.Err("ERR Mandatory argument FIELDS is missing or not at the right position")
		return nil, false
	}
	if i+1 >= len(c.Args) {
		c.WrongArgs()
		return nil, false
	}
	n, ok := parseInt(c.Args[i+1])
	if !ok || n < 1 {
		c.Err("ERR Parameter `numFields` should be greater than 0")
		return nil, false
	}
	if n != int64(len(c.Args)-i-2) {
		c.Err("ERR The `numfields` parameter must match the number of arguments")
		return nil, false
	}
	fs := make([]string, n)
	for j := range fs {
		fs[j] = c.Arg(i + 2 + j)
	}
	return fs, true
}

const hfeMaxAbsTime = (1 << 48) - 1

func hexpireGeneric(c *Ctx, unitSec, absolute bool) {
	key := c.Arg(1)
	when, ok := c.Int(2)
	if !ok {
		return
	}
	pos := 3
	var nx, xx, gt, lt bool
	if pos < len(c.Args) {
		switch strings.ToLower(c.Arg(pos)) {
		case "nx":
			nx = true
			pos++
		case "xx":
			xx = true
			pos++
		case "gt":
			gt = true
			pos++
		case "lt":
			lt = true
			pos++
		}
	}
	fields, ok := parseFields(c, pos)
	if !ok {
		return
	}
	if when < 0 {
		c.Err("ERR invalid expire time, must be >= 0")
		return
	}
	if unitSec {
		if when > hfeMaxAbsTime/1000 {
			c.Err("ERR invalid expire time in '" + c.Cmd.Name + "' command")
			return
		}
		when *= 1000
	}
	if !absolute {
		when += time.Now().UnixMilli()
	}
	if when > hfeMaxAbsTime {
		c.Err("ERR invalid expire time in '" + c.Cmd.Name + "' command")
		return
	}
	h, ok := c.getHash(key)
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(fields))
	if h == nil {
		for range fields {
			c.W.WriteInt(-2)
		}
		return
	}
	var changed int64
	now := time.Now().UnixMilli()
	for _, f := range fields {
		cur, exists := h.TTL(f, c.DB.Now)
		if !exists {
			c.W.WriteInt(-2)
			continue
		}
		if (nx && cur != 0) || (xx && cur == 0) || (gt && (cur == 0 || when <= cur)) || (lt && cur != 0 && when >= cur) {
			c.W.WriteInt(0)
			continue
		}
		if when <= now {
			h.Del(f, c.DB.Now)
			c.W.WriteInt(2)
			changed++
			continue
		}
		h.SetTTL(f, when)
		c.W.WriteInt(1)
		changed++
	}
	if changed > 0 {
		if h.RawLen() == 0 {
			c.DB.Delete(key)
		}
		c.Dirty(changed)
		args := [][]byte{[]byte("HPEXPIREAT"), c.Args[1], formatInt(when), []byte("FIELDS"), formatInt(int64(len(fields)))}
		for _, f := range fields {
			args = append(args, []byte(f))
		}
		c.Rewrite(args...)
	}
}

func cmdHExpire(c *Ctx)    { hexpireGeneric(c, true, false) }
func cmdHPExpire(c *Ctx)   { hexpireGeneric(c, false, false) }
func cmdHExpireAt(c *Ctx)  { hexpireGeneric(c, true, true) }
func cmdHPExpireAt(c *Ctx) { hexpireGeneric(c, false, true) }

func httlGeneric(c *Ctx, ms, absolute bool) {
	fields, ok := parseFields(c, 2)
	if !ok {
		return
	}
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(fields))
	now := time.Now().UnixMilli()
	for _, f := range fields {
		if h == nil {
			c.W.WriteInt(-2)
			continue
		}
		at, exists := h.TTL(f, c.DB.Now)
		switch {
		case !exists:
			c.W.WriteInt(-2)
		case at == 0:
			c.W.WriteInt(-1)
		case absolute && ms:
			c.W.WriteInt(at)
		case absolute:
			c.W.WriteInt(at / 1000)
		case ms:
			c.W.WriteInt(at - now)
		default:
			c.W.WriteInt((at - now + 999) / 1000)
		}
	}
}

func cmdHTTL(c *Ctx)         { httlGeneric(c, false, false) }
func cmdHPTTL(c *Ctx)        { httlGeneric(c, true, false) }
func cmdHExpireTime(c *Ctx)  { httlGeneric(c, false, true) }
func cmdHPExpireTime(c *Ctx) { httlGeneric(c, true, true) }

func cmdHPersist(c *Ctx) {
	fields, ok := parseFields(c, 2)
	if !ok {
		return
	}
	h, ok := c.getHash(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(fields))
	var changed int64
	for _, f := range fields {
		if h == nil {
			c.W.WriteInt(-2)
			continue
		}
		at, exists := h.TTL(f, c.DB.Now)
		switch {
		case !exists:
			c.W.WriteInt(-2)
		case at == 0:
			c.W.WriteInt(-1)
		default:
			h.SetTTL(f, 0)
			changed++
			c.W.WriteInt(1)
		}
	}
	c.Dirty(changed)
}

func cmdHGetDel(c *Ctx) {
	key := c.Arg(1)
	fields, ok := parseFields(c, 2)
	if !ok {
		return
	}
	h, ok := c.getHash(key)
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(fields))
	var n int64
	for _, f := range fields {
		if h == nil {
			c.W.WriteNull()
			continue
		}
		v, exists := h.Get(f, c.DB.Now)
		if !exists {
			c.W.WriteNull()
			continue
		}
		c.W.WriteBulk(v)
		h.Del(f, c.DB.Now)
		n++
	}
	if h != nil && h.RawLen() == 0 {
		c.DB.Delete(key)
	}
	if n > 0 {
		c.Dirty(n)
		args := [][]byte{[]byte("HDEL"), c.Args[1]}
		for _, f := range fields {
			args = append(args, []byte(f))
		}
		c.Rewrite(args...)
	}
}

func cmdHGetEx(c *Ctx) {
	key := c.Arg(1)
	pos := 2
	var at int64
	persist, setTTL := false, false
	if pos < len(c.Args) {
		opt := strings.ToLower(c.Arg(pos))
		switch opt {
		case "ex", "px", "exat", "pxat":
			if pos+1 >= len(c.Args) {
				c.SyntaxErr()
				return
			}
			v, ok := c.parseExpireArg(opt, c.Args[pos+1], "hgetex")
			if !ok {
				return
			}
			at, setTTL = v, true
			pos += 2
		case "persist":
			persist = true
			pos++
		}
	}
	fields, ok := parseFields(c, pos)
	if !ok {
		return
	}
	h, ok := c.getHash(key)
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(fields))
	var changed int64
	for _, f := range fields {
		if h == nil {
			c.W.WriteNull()
			continue
		}
		v, exists := h.Get(f, c.DB.Now)
		if !exists {
			c.W.WriteNull()
			continue
		}
		c.W.WriteBulk(v)
		if setTTL {
			h.SetTTL(f, at)
			changed++
		} else if persist {
			if cur, _ := h.TTL(f, c.DB.Now); cur != 0 {
				h.SetTTL(f, 0)
				changed++
			}
		}
	}
	if changed > 0 {
		c.Dirty(changed)
		var args [][]byte
		if setTTL {
			args = [][]byte{[]byte("HPEXPIREAT"), c.Args[1], formatInt(at), []byte("FIELDS"), formatInt(int64(len(fields)))}
		} else {
			args = [][]byte{[]byte("HPERSIST"), c.Args[1], []byte("FIELDS"), formatInt(int64(len(fields)))}
		}
		for _, f := range fields {
			args = append(args, []byte(f))
		}
		c.Rewrite(args...)
	}
}

func cmdHSetEx(c *Ctx) {
	key := c.Arg(1)
	pos := 2
	var fnx, fxx, keepTTL, setTTL bool
	var at int64
	for pos < len(c.Args) && !c.Is(pos, "fields") {
		opt := strings.ToLower(c.Arg(pos))
		switch opt {
		case "fnx":
			fnx = true
			pos++
		case "fxx":
			fxx = true
			pos++
		case "keepttl":
			keepTTL = true
			pos++
		case "ex", "px", "exat", "pxat":
			if pos+1 >= len(c.Args) {
				c.SyntaxErr()
				return
			}
			v, ok := c.parseExpireArg(opt, c.Args[pos+1], "hsetex")
			if !ok {
				return
			}
			at, setTTL = v, true
			pos += 2
		default:
			c.SyntaxErr()
			return
		}
	}
	if pos+1 >= len(c.Args) || !c.Is(pos, "fields") {
		c.Err("ERR Mandatory argument FIELDS is missing or not at the right position")
		return
	}
	n, ok := parseInt(c.Args[pos+1])
	if !ok || n < 1 || n*2 != int64(len(c.Args)-pos-2) {
		c.Err("ERR invalid number of fields")
		return
	}
	h, ok := c.getHash(key)
	if !ok {
		return
	}
	pairs := c.Args[pos+2:]
	if fnx || fxx {
		for i := 0; i < len(pairs); i += 2 {
			exists := false
			if h != nil {
				_, exists = h.Get(string(pairs[i]), c.DB.Now)
			}
			if (fnx && exists) || (fxx && !exists) {
				c.W.WriteInt(0)
				return
			}
		}
	}
	if h == nil {
		h = ds.NewHash()
		c.DB.Add(key, h)
	}
	for i := 0; i < len(pairs); i += 2 {
		f := string(pairs[i])
		if keepTTL {
			h.SetKeepTTL(f, pairs[i+1])
		} else {
			h.Set(f, pairs[i+1], c.DB.Now)
		}
		if setTTL {
			h.SetTTL(f, at)
		}
	}
	c.Dirty(n)
	if setTTL {
		args := [][]byte{[]byte("HSETEX"), c.Args[1], []byte("PXAT"), formatInt(at), []byte("FIELDS"), c.Args[pos+1]}
		args = append(args, pairs...)
		c.Rewrite(args...)
	}
	c.W.WriteInt(1)
}
