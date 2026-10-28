package server

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/storage"
)

const maxStringSize = 512 * 1024 * 1024

// getStr looks up a string key. ok=false means a WRONGTYPE reply was sent.
func (c *Ctx) getStr(key string) (val []byte, e *storage.Entry, ok bool) {
	e = c.Lookup(key)
	if e == nil {
		return nil, nil, true
	}
	b, isStr := e.Value.([]byte)
	if !isStr {
		c.WrongType()
		return nil, nil, false
	}
	return b, e, true
}

// expireOpts holds parsed EX/PX/EXAT/PXAT/KEEPTTL/PERSIST options.
type expireOpts struct {
	at      int64 // absolute ms, 0 = none
	keepTTL bool
	persist bool
	set     bool
}

// parseExpireArg parses the value for EX/PX/EXAT/PXAT. cmdName is used for
// the "invalid expire time in '<cmd>' command" error.
func (c *Ctx) parseExpireArg(unit string, raw []byte, cmdName string) (int64, bool) {
	n, ok := parseInt(raw)
	if !ok {
		c.Err(errNotInt)
		return 0, false
	}
	bad := func() (int64, bool) {
		c.Err("ERR invalid expire time in '" + cmdName + "' command")
		return 0, false
	}
	if n <= 0 {
		return bad()
	}
	ms := n
	if unit == "ex" || unit == "exat" {
		if n > math.MaxInt64/1000 {
			return bad()
		}
		ms = n * 1000
	}
	if unit == "ex" || unit == "px" {
		now := time.Now().UnixMilli()
		if ms > math.MaxInt64-now {
			return bad()
		}
		ms += now
	}
	return ms, true
}

func cmdGet(c *Ctx) {
	v, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	if e == nil {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(v)
}

func cmdSet(c *Ctx) {
	key := c.Arg(1)
	val := c.Args[2]
	var nx, xx, get bool
	var exp expireOpts
	var expUnit string
	for i := 3; i < len(c.Args); i++ {
		opt := strings.ToLower(c.Arg(i))
		switch {
		case opt == "nx" && !xx:
			nx = true
		case opt == "xx" && !nx:
			xx = true
		case opt == "get":
			get = true
		case opt == "keepttl" && !exp.set:
			exp.keepTTL = true
		case (opt == "ex" || opt == "px" || opt == "exat" || opt == "pxat") && !exp.set && !exp.keepTTL && i+1 < len(c.Args):
			exp.set = true
			expUnit = opt
			at, ok := c.parseExpireArg(opt, c.Args[i+1], "set")
			if !ok {
				return
			}
			exp.at = at
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	_ = expUnit
	old, e, ok := c.getStr(key)
	if !ok {
		return
	}
	if (nx && e != nil) || (xx && e == nil) {
		if get {
			if e == nil {
				c.W.WriteNull()
			} else {
				c.W.WriteBulk(old)
			}
		} else {
			c.W.WriteNull()
		}
		return
	}
	if exp.set && exp.at <= c.DB.Now && !c.isMaster {
		// Already expired: delete (if present) and propagate a DEL.
		if c.DB.Delete(key) {
			c.Dirty(1)
			c.RewriteStrings("DEL", key)
		} else {
			c.Dirty(1)
			c.NoPropagate()
		}
		if get {
			if e == nil {
				c.W.WriteNull()
			} else {
				c.W.WriteBulk(old)
			}
		} else {
			c.W.WriteOK()
		}
		return
	}
	ne := c.DB.SetValue(key, val, exp.keepTTL)
	if exp.set {
		c.DB.SetExpire(key, ne, exp.at)
	}
	c.Dirty(1)
	if exp.set {
		c.Rewrite([]byte("SET"), c.Args[1], val, []byte("PXAT"), formatInt(exp.at))
	} else if exp.keepTTL {
		c.Rewrite([]byte("SET"), c.Args[1], val, []byte("KEEPTTL"))
	} else {
		c.Rewrite([]byte("SET"), c.Args[1], val)
	}
	if get {
		if e == nil {
			c.W.WriteNull()
		} else {
			c.W.WriteBulk(old)
		}
		return
	}
	c.W.WriteOK()
}

func cmdSetNX(c *Ctx) {
	key := c.Arg(1)
	if c.DB.Lookup(key) != nil {
		c.W.WriteInt(0)
		return
	}
	c.DB.Add(key, c.Args[2])
	c.Dirty(1)
	c.W.WriteInt(1)
}

func setexGeneric(c *Ctx, unit string) {
	at, ok := c.parseExpireArg(unit, c.Args[2], strings.ToLower(c.Cmd.Name))
	if !ok {
		return
	}
	key := c.Arg(1)
	e := c.DB.SetValue(key, c.Args[3], false)
	c.DB.SetExpire(key, e, at)
	c.Dirty(1)
	c.Rewrite([]byte("SET"), c.Args[1], c.Args[3], []byte("PXAT"), formatInt(at))
	c.W.WriteOK()
}

func cmdSetEX(c *Ctx)  { setexGeneric(c, "ex") }
func cmdPSetEX(c *Ctx) { setexGeneric(c, "px") }

func cmdGetSet(c *Ctx) {
	old, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	c.DB.SetValue(c.Arg(1), c.Args[2], false)
	c.Dirty(1)
	c.Rewrite([]byte("SET"), c.Args[1], c.Args[2])
	if e == nil {
		c.W.WriteNull()
	} else {
		c.W.WriteBulk(old)
	}
}

func cmdGetDel(c *Ctx) {
	v, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	if e == nil {
		c.W.WriteNull()
		return
	}
	c.DB.Delete(c.Arg(1))
	c.Dirty(1)
	c.RewriteStrings("DEL", c.Arg(1))
	c.W.WriteBulk(v)
}

func cmdGetEx(c *Ctx) {
	key := c.Arg(1)
	var exp expireOpts
	for i := 2; i < len(c.Args); i++ {
		opt := strings.ToLower(c.Arg(i))
		switch {
		case opt == "persist" && !exp.set && !exp.persist:
			exp.persist = true
		case (opt == "ex" || opt == "px" || opt == "exat" || opt == "pxat") && !exp.set && !exp.persist && i+1 < len(c.Args):
			at, ok := c.parseExpireArg(opt, c.Args[i+1], "getex")
			if !ok {
				return
			}
			exp.set = true
			exp.at = at
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	if e == nil {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(v)
	if exp.set {
		if exp.at <= c.DB.Now && !c.isMaster {
			c.DB.Delete(key)
			c.Dirty(1)
			c.RewriteStrings("DEL", key)
			return
		}
		c.DB.SetExpire(key, e, exp.at)
		c.Dirty(1)
		c.Rewrite([]byte("PEXPIREAT"), c.Args[1], formatInt(exp.at))
	} else if exp.persist && e.ExpireAt != 0 {
		c.DB.SetExpire(key, e, 0)
		c.Dirty(1)
		c.Rewrite([]byte("PERSIST"), c.Args[1])
	}
}

func cmdMGet(c *Ctx) {
	c.W.WriteArrayLen(len(c.Args) - 1)
	for _, k := range c.Args[1:] {
		e := c.Lookup(string(k))
		if e == nil {
			c.W.WriteNull()
			continue
		}
		if b, ok := e.Value.([]byte); ok {
			c.W.WriteBulk(b)
		} else {
			c.W.WriteNull()
		}
	}
}

func cmdMSet(c *Ctx) {
	if len(c.Args)%2 != 1 {
		c.WrongArgs()
		return
	}
	for i := 1; i < len(c.Args); i += 2 {
		c.DB.SetValue(c.Arg(i), c.Args[i+1], false)
	}
	c.Dirty(int64(len(c.Args) / 2))
	c.W.WriteOK()
}

func cmdMSetNX(c *Ctx) {
	if len(c.Args)%2 != 1 {
		c.WrongArgs()
		return
	}
	for i := 1; i < len(c.Args); i += 2 {
		if c.DB.Lookup(c.Arg(i)) != nil {
			c.W.WriteInt(0)
			return
		}
	}
	for i := 1; i < len(c.Args); i += 2 {
		c.DB.SetValue(c.Arg(i), c.Args[i+1], false)
	}
	c.Dirty(int64(len(c.Args) / 2))
	c.W.WriteInt(1)
}

func cmdStrlen(c *Ctx) {
	v, _, ok := c.getStr(c.Arg(1))
	if ok {
		c.W.WriteInt(int64(len(v)))
	}
}

func cmdAppend(c *Ctx) {
	key := c.Arg(1)
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	if e == nil {
		c.DB.Add(key, c.Args[2])
		c.Dirty(1)
		c.W.WriteInt(int64(len(c.Args[2])))
		return
	}
	if int64(len(v))+int64(len(c.Args[2])) > maxStringSize {
		c.Err("ERR string exceeds maximum allowed size (proto-max-bulk-len)")
		return
	}
	nv := make([]byte, 0, growCap(len(v)+len(c.Args[2])))
	nv = append(append(nv, v...), c.Args[2]...)
	e.Value = nv
	c.Dirty(1)
	c.W.WriteInt(int64(len(nv)))
}

func growCap(n int) int {
	if n < 64 {
		return n
	}
	return n + n/4
}

func cmdGetRange(c *Ctx) {
	start, ok := c.Int(2)
	if !ok {
		return
	}
	end, ok := c.Int(3)
	if !ok {
		return
	}
	v, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	if e == nil {
		c.W.WriteBulkString("")
		return
	}
	n := int64(len(v))
	if start < 0 && end < 0 && start > end {
		c.W.WriteBulkString("")
		return
	}
	if start < 0 {
		start = n + start
	}
	if end < 0 {
		end = n + end
	}
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if end >= n {
		end = n - 1
	}
	if start > end || n == 0 {
		c.W.WriteBulkString("")
		return
	}
	c.W.WriteBulk(v[start : end+1])
}

func cmdSetRange(c *Ctx) {
	key := c.Arg(1)
	off, ok := c.Int(2)
	if !ok {
		return
	}
	if off < 0 {
		c.Err("ERR offset is out of range")
		return
	}
	val := c.Args[3]
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	if e == nil {
		if len(val) == 0 {
			c.W.WriteInt(0)
			return
		}
	} else if len(val) == 0 {
		c.W.WriteInt(int64(len(v)))
		return
	}
	if off+int64(len(val)) > maxStringSize {
		c.Err("ERR string exceeds maximum allowed size (proto-max-bulk-len)")
		return
	}
	need := int(off) + len(val)
	nv := v
	if need > len(v) {
		nv = make([]byte, need)
		copy(nv, v)
	} else {
		nv = append([]byte(nil), v...)
	}
	copy(nv[off:], val)
	if e == nil {
		c.DB.Add(key, nv)
	} else {
		e.Value = nv
	}
	c.Dirty(1)
	c.W.WriteInt(int64(len(nv)))
}

func incrGeneric(c *Ctx, delta int64) {
	key := c.Arg(1)
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	var cur int64
	if e != nil {
		n, isInt := parseInt(v)
		if !isInt {
			c.Err(errNotInt)
			return
		}
		cur = n
	}
	if (delta < 0 && cur < 0 && delta < math.MinInt64-cur) || (delta > 0 && cur > 0 && delta > math.MaxInt64-cur) {
		c.Err(errOverflow)
		return
	}
	cur += delta
	nb := formatInt(cur)
	if e == nil {
		c.DB.Add(key, nb)
	} else {
		e.Value = nb
	}
	c.Dirty(1)
	c.W.WriteInt(cur)
}

func cmdIncr(c *Ctx) { incrGeneric(c, 1) }
func cmdDecr(c *Ctx) { incrGeneric(c, -1) }
func cmdIncrBy(c *Ctx) {
	d, ok := c.Int(2)
	if ok {
		incrGeneric(c, d)
	}
}
func cmdDecrBy(c *Ctx) {
	d, ok := c.Int(2)
	if !ok {
		return
	}
	if d == math.MinInt64 {
		c.Err("ERR decrement would overflow")
		return
	}
	incrGeneric(c, -d)
}

// formatLongDouble renders numbers like Redis' ld2string(LD_STR_HUMAN):
// fixed notation without trailing zeros.
func formatLongDouble(f float64) string {
	if math.IsInf(f, 0) {
		if f > 0 {
			return "inf"
		}
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if s == "-0" {
		s = "0"
	}
	return s
}

func cmdIncrByFloat(c *Ctx) {
	key := c.Arg(1)
	incr, ok := parseFloat(c.Args[2])
	if !ok || math.IsInf(incr, 0) {
		c.Err(errNotFloat)
		return
	}
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	var cur float64
	if e != nil {
		f, isF := parseFloat(v)
		if !isF || math.IsInf(f, 0) {
			c.Err(errNotFloat)
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
	if e == nil {
		c.DB.Add(key, nb)
	} else {
		e.Value = nb
	}
	c.Dirty(1)
	c.Rewrite([]byte("SET"), c.Args[1], nb, []byte("KEEPTTL"))
	c.W.WriteBulk(nb)
}

func cmdLCS(c *Ctx) {
	var getLen, getIdx, withMatchLen bool
	var minMatch int64
	for i := 3; i < len(c.Args); i++ {
		opt := strings.ToLower(c.Arg(i))
		switch {
		case opt == "idx":
			getIdx = true
		case opt == "len":
			getLen = true
		case opt == "withmatchlen":
			withMatchLen = true
		case opt == "minmatchlen" && i+1 < len(c.Args):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				n = 0
			}
			minMatch = n
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	a, ea, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	b, eb, ok := c.getStr(c.Arg(2))
	if !ok {
		return
	}
	_ = ea
	_ = eb
	if getIdx && getLen {
		c.Err("ERR If you want both the length and indexes, please just use IDX.")
		return
	}
	alen, blen := len(a), len(b)
	if uint64(alen+1)*uint64(blen+1)*4 > uint64(c.s.cfg.ProtoMaxBulkLen.Load()) {
		c.Err("ERR Insufficient memory, transient memory for LCS exceeds proto-max-bulk-len")
		return
	}
	tbl := make([]uint32, (alen+1)*(blen+1))
	at := func(i, j int) *uint32 { return &tbl[j+i*(blen+1)] }
	for i := 0; i <= alen; i++ {
		for j := 0; j <= blen; j++ {
			if i == 0 || j == 0 {
				*at(i, j) = 0
			} else if a[i-1] == b[j-1] {
				*at(i, j) = *at(i-1, j-1) + 1
			} else {
				l1, l2 := *at(i-1, j), *at(i, j-1)
				if l1 > l2 {
					*at(i, j) = l1
				} else {
					*at(i, j) = l2
				}
			}
		}
	}
	idx := int(*at(alen, blen))
	result := make([]byte, idx)
	type match struct{ as, ae, bs, be, l int }
	var matches []match
	arange0, arange1, brange0, brange1 := -1, -1, -1, -1
	i, j := alen, blen
	for i > 0 && j > 0 {
		emitRange := false
		if a[i-1] == b[j-1] {
			result[idx-1] = a[i-1]
			if arange0 == -1 {
				arange0, arange1 = i-1, i-1
				brange0, brange1 = j-1, j-1
			} else if arange0 == i && brange0 == j {
				arange0--
				brange0--
			} else {
				emitRange = true
			}
			if arange0 == 0 || brange0 == 0 {
				emitRange = true
			}
			idx--
			i--
			j--
		} else {
			l1, l2 := *at(i-1, j), *at(i, j-1)
			if l1 > l2 {
				i--
			} else {
				j--
			}
			if arange0 != -1 {
				emitRange = true
			}
		}
		if emitRange {
			ml := arange1 - arange0 + 1
			if minMatch == 0 || int64(ml) >= minMatch {
				matches = append(matches, match{arange0, arange1, brange0, brange1, ml})
			}
			arange0 = -1
		}
	}
	total := int64(*at(alen, blen))
	if getIdx {
		c.W.WriteMapLen(2)
		c.W.WriteBulkString("matches")
		c.W.WriteArrayLen(len(matches))
		for _, m := range matches {
			if withMatchLen {
				c.W.WriteArrayLen(3)
			} else {
				c.W.WriteArrayLen(2)
			}
			c.W.WriteArrayLen(2)
			c.W.WriteInt(int64(m.as))
			c.W.WriteInt(int64(m.ae))
			c.W.WriteArrayLen(2)
			c.W.WriteInt(int64(m.bs))
			c.W.WriteInt(int64(m.be))
			if withMatchLen {
				c.W.WriteInt(int64(m.l))
			}
		}
		c.W.WriteBulkString("len")
		c.W.WriteInt(total)
		return
	}
	if getLen {
		c.W.WriteInt(total)
		return
	}
	c.W.WriteBulk(result)
}
