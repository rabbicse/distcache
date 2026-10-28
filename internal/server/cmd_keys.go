package server

import (
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/storage"
)

func cmdDel(c *Ctx) {
	var n int64
	for _, k := range c.Args[1:] {
		key := string(k)
		if c.DB.Lookup(key) != nil {
			c.DB.Delete(key)
			n++
		}
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdExists(c *Ctx) {
	var n int64
	for _, k := range c.Args[1:] {
		if c.Lookup(string(k)) != nil {
			n++
		}
	}
	c.W.WriteInt(n)
}

func cmdTouch(c *Ctx) { cmdExists(c) }

func cmdType(c *Ctx) {
	c.DB.NoTouch = true
	e := c.DB.Lookup(c.Arg(1))
	if e == nil {
		c.W.WriteSimple(storage.TypeNone)
		return
	}
	c.W.WriteSimple(storage.TypeName(e.Value))
}

// expireGeneric implements EXPIRE/PEXPIRE/EXPIREAT/PEXPIREAT.
func expireGeneric(c *Ctx, unitSeconds, absolute bool) {
	key := c.Arg(1)
	when, ok := c.Int(2)
	if !ok {
		return
	}
	var nx, xx, gt, lt bool
	for i := 3; i < len(c.Args); i++ {
		switch strings.ToLower(c.Arg(i)) {
		case "nx":
			nx = true
		case "xx":
			xx = true
		case "gt":
			gt = true
		case "lt":
			lt = true
		default:
			c.Err("ERR Unsupported option " + c.Arg(i))
			return
		}
	}
	if nx && (xx || gt || lt) {
		c.Err("ERR NX and XX, GT or LT options at the same time are not compatible")
		return
	}
	if gt && lt {
		c.Err("ERR GT and LT options at the same time are not compatible")
		return
	}
	cmdName := strings.ToLower(c.Cmd.Name)
	if unitSeconds {
		if when > math.MaxInt64/1000 || when < math.MinInt64/1000 {
			c.Err("ERR invalid expire time in '" + cmdName + "' command")
			return
		}
		when *= 1000
	}
	if !absolute {
		now := time.Now().UnixMilli()
		if when > math.MaxInt64-now {
			c.Err("ERR invalid expire time in '" + cmdName + "' command")
			return
		}
		when += now
	}
	e := c.DB.Lookup(key)
	if e == nil {
		c.W.WriteInt(0)
		return
	}
	cur := e.ExpireAt
	if nx && cur != 0 || xx && cur == 0 {
		c.W.WriteInt(0)
		return
	}
	if gt && (cur == 0 || when <= cur) {
		c.W.WriteInt(0)
		return
	}
	if lt && cur != 0 && when >= cur {
		c.W.WriteInt(0)
		return
	}
	if when <= time.Now().UnixMilli() && !c.isMaster && !c.s.loading.Load() {
		c.DB.Delete(key)
		c.Dirty(1)
		c.RewriteStrings("DEL", key)
		c.s.notify(config.NotifyGeneric, "del", key, c.DB.Idx)
		c.W.WriteInt(1)
		return
	}
	c.DB.SetExpire(key, e, when)
	c.Dirty(1)
	c.Rewrite([]byte("PEXPIREAT"), c.Args[1], formatInt(when))
	c.W.WriteInt(1)
}

func cmdExpire(c *Ctx)    { expireGeneric(c, true, false) }
func cmdPExpire(c *Ctx)   { expireGeneric(c, false, false) }
func cmdExpireAt(c *Ctx)  { expireGeneric(c, true, true) }
func cmdPExpireAt(c *Ctx) { expireGeneric(c, false, true) }

func ttlGeneric(c *Ctx, ms, absolute bool) {
	c.DB.NoTouch = true
	e := c.DB.Lookup(c.Arg(1))
	if e == nil {
		c.W.WriteInt(-2)
		return
	}
	if e.ExpireAt == 0 {
		c.W.WriteInt(-1)
		return
	}
	if absolute {
		if ms {
			c.W.WriteInt(e.ExpireAt)
		} else {
			c.W.WriteInt(e.ExpireAt / 1000)
		}
		return
	}
	ttl := e.ExpireAt - time.Now().UnixMilli()
	if ttl < 0 {
		ttl = 0
	}
	if ms {
		c.W.WriteInt(ttl)
	} else {
		c.W.WriteInt((ttl + 500) / 1000)
	}
}

func cmdTTL(c *Ctx)         { ttlGeneric(c, false, false) }
func cmdPTTL(c *Ctx)        { ttlGeneric(c, true, false) }
func cmdExpireTime(c *Ctx)  { ttlGeneric(c, false, true) }
func cmdPExpireTime(c *Ctx) { ttlGeneric(c, true, true) }

func cmdPersist(c *Ctx) {
	e := c.DB.Lookup(c.Arg(1))
	if e == nil || e.ExpireAt == 0 {
		c.W.WriteInt(0)
		return
	}
	c.DB.SetExpire(c.Arg(1), e, 0)
	c.Dirty(1)
	c.W.WriteInt(1)
}

func cmdKeys(c *Ctx) {
	pat := c.Arg(1)
	all := pat == "*"
	var out []string
	c.forEachShard(false, func(i int) bool {
		db := storage.DB{KS: c.s.ks, Idx: c.DB.Idx, Now: time.Now().UnixMilli(), IgnoreExpiry: c.DB.IgnoreExpiry}
		c.s.ks.ForEachInShard(i, c.DB.Idx, func(k string, e *storage.Entry) bool {
			if db.IsExpired(e) {
				return true
			}
			if all || ds.Match(pat, k, false) {
				out = append(out, k)
			}
			return true
		})
		return true
	})
	c.W.WriteStrings(out)
}

func parseCursor(b []byte) (uint64, bool) {
	n, err := strconv.ParseUint(string(b), 10, 64)
	return n, err == nil
}

func cmdScan(c *Ctx) {
	cursor, ok := parseCursor(c.Args[1])
	if !ok {
		c.Err("ERR invalid cursor")
		return
	}
	var pattern, typ string
	count := int64(10)
	for i := 2; i < len(c.Args); i++ {
		opt := strings.ToLower(c.Arg(i))
		if i+1 >= len(c.Args) {
			c.SyntaxErr()
			return
		}
		switch opt {
		case "match":
			pattern = c.Arg(i + 1)
		case "count":
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 1 {
				c.SyntaxErr()
				return
			}
			count = n
		case "type":
			typ = strings.ToLower(c.Arg(i + 1))
		default:
			c.SyntaxErr()
			return
		}
		i++
	}
	n := uint64(c.s.ks.NumShards())
	if cursor >= n {
		c.W.WriteArrayLen(2)
		c.W.WriteBulkString("0")
		c.W.WriteArrayLen(0)
		return
	}
	var keys []string
	next := cursor
	for next < n && int64(len(keys)) < count {
		i := int(next)
		c.withShard(i, false, func() {
			db := storage.DB{KS: c.s.ks, Idx: c.DB.Idx, Now: time.Now().UnixMilli(), IgnoreExpiry: c.DB.IgnoreExpiry}
			c.s.ks.ForEachInShard(i, c.DB.Idx, func(k string, e *storage.Entry) bool {
				if db.IsExpired(e) {
					return true
				}
				if pattern != "" && pattern != "*" && !ds.Match(pattern, k, false) {
					return true
				}
				if typ != "" && storage.TypeName(e.Value) != typ {
					return true
				}
				keys = append(keys, k)
				return true
			})
		})
		next++
	}
	if next >= n {
		next = 0
	}
	c.W.WriteArrayLen(2)
	c.W.WriteBulkString(strconv.FormatUint(next, 10))
	c.W.WriteStrings(keys)
}

func cmdRandomKey(c *Ctx) {
	if c.s.ks.DBSize(c.DB.Idx) == 0 {
		c.W.WriteNull()
		return
	}
	n := c.s.ks.NumShards()
	for attempt := 0; attempt < 4*n; attempt++ {
		i := rand.IntN(n)
		var found string
		var ok bool
		c.withShard(i, false, func() {
			db := storage.DB{KS: c.s.ks, Idx: c.DB.Idx, Now: time.Now().UnixMilli(), IgnoreExpiry: c.DB.IgnoreExpiry}
			for _, k := range c.s.ks.SampleShard(i, c.DB.Idx, 8, false) {
				if e := db.Peek(k); e != nil && !db.IsExpired(e) {
					found, ok = k, true
					return
				}
			}
		})
		if ok {
			c.W.WriteBulkString(found)
			return
		}
	}
	c.W.WriteNull()
}

func renameGeneric(c *Ctx, nx bool) {
	src, dst := c.Arg(1), c.Arg(2)
	e := c.DB.Lookup(src)
	if e == nil {
		c.Err(errNoSuchKey)
		return
	}
	if src == dst {
		if nx {
			c.W.WriteInt(0)
		} else {
			c.W.WriteOK()
		}
		return
	}
	if nx && c.DB.Lookup(dst) != nil {
		c.W.WriteInt(0)
		return
	}
	at := e.ExpireAt
	v := e.Value
	c.DB.Delete(src)
	ne := c.s.ks.NewEntry(v)
	ne.ExpireAt = at
	c.DB.Set(dst, ne)
	c.Dirty(1)
	c.s.notify(config.NotifyGeneric, "rename_from", src, c.DB.Idx)
	c.s.notify(config.NotifyGeneric, "rename_to", dst, c.DB.Idx)
	if nx {
		c.W.WriteInt(1)
	} else {
		c.W.WriteOK()
	}
}

func cmdRename(c *Ctx)   { renameGeneric(c, false) }
func cmdRenameNX(c *Ctx) { renameGeneric(c, true) }

func (c *Ctx) parseDBIndex(i int) (int, bool) {
	n, ok := parseInt(c.Args[i])
	if !ok {
		c.Err(errNotInt)
		return 0, false
	}
	if n < 0 || n >= int64(c.s.ks.NumDB) {
		c.Err(errDBIndex)
		return 0, false
	}
	return int(n), true
}

func cmdMove(c *Ctx) {
	key := c.Arg(1)
	dbi, ok := c.parseDBIndex(2)
	if !ok {
		return
	}
	if dbi == c.DB.Idx {
		c.Err("ERR source and destination objects are the same")
		return
	}
	e := c.DB.Lookup(key)
	if e == nil {
		c.W.WriteInt(0)
		return
	}
	dst := *c.DB
	dst.Idx = dbi
	if dst.Lookup(key) != nil {
		c.W.WriteInt(0)
		return
	}
	dst.Preserve(key)
	c.DB.Delete(key)
	ne := c.s.ks.NewEntry(e.Value)
	ne.ExpireAt = e.ExpireAt
	dst.Set(key, ne)
	c.Dirty(1)
	c.s.watches.touch(dbi, []string{key})
	c.s.blocker.signal(dbi, []string{key})
	c.W.WriteInt(1)
}

func cmdCopy(c *Ctx) {
	src, dstKey := c.Arg(1), c.Arg(2)
	dst := *c.DB
	replace := false
	for i := 3; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "replace"):
			replace = true
		case c.Is(i, "db") && i+1 < len(c.Args):
			dbi, ok := c.parseDBIndex(i + 1)
			if !ok {
				return
			}
			dst.Idx = dbi
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	if src == dstKey && dst.Idx == c.DB.Idx {
		c.Err("ERR source and destination objects are the same")
		return
	}
	e := c.DB.Lookup(src)
	if e == nil {
		c.W.WriteInt(0)
		return
	}
	if dst.Lookup(dstKey) != nil {
		if !replace {
			c.W.WriteInt(0)
			return
		}
	}
	if dst.Idx != c.DB.Idx {
		dst.Preserve(dstKey)
	}
	ne := c.s.ks.NewEntry(storage.CloneValue(e.Value))
	ne.ExpireAt = e.ExpireAt
	dst.Set(dstKey, ne)
	c.Dirty(1)
	if dst.Idx != c.DB.Idx {
		dst.Updated(dstKey)
		c.s.watches.touch(dst.Idx, []string{dstKey})
		c.s.blocker.signal(dst.Idx, []string{dstKey})
	}
	c.W.WriteInt(1)
}

// objectEncoding mirrors Redis encodings for OBJECT ENCODING.
func (s *Server) objectEncoding(v any) string {
	cfg := s.cfg
	switch x := v.(type) {
	case []byte:
		if len(x) <= 20 {
			if _, ok := parseInt(x); ok {
				return "int"
			}
		}
		if len(x) <= 44 {
			return "embstr"
		}
		return "raw"
	case *ds.List:
		if x.Len() <= 128 && x.Bytes() <= 8192+int64(cap([]int{}))+int64(x.Len()*ds.ListElemOverhead)*4 {
			small := true
			var total int64
			for i := 0; i < x.Len(); i++ {
				total += int64(len(x.Index(i)))
				if total > 8192 {
					small = false
					break
				}
			}
			if small {
				return "listpack"
			}
		}
		return "quicklist"
	case *ds.Hash:
		if int64(x.RawLen()) > cfg.HashMaxEntries.Load() {
			return "hashtable"
		}
		big := false
		x.ForEach(math.MinInt64, func(f string, val []byte) bool {
			if int64(len(f)) > cfg.HashMaxValue.Load() || int64(len(val)) > cfg.HashMaxValue.Load() {
				big = true
				return false
			}
			return true
		})
		if big {
			return "hashtable"
		}
		if x.HasTTLs() {
			return "listpackex"
		}
		return "listpack"
	case *ds.Set:
		allInt := true
		x.ForEach(func(m string) bool {
			if _, ok := parseInt([]byte(m)); !ok {
				allInt = false
				return false
			}
			return true
		})
		if allInt && int64(x.Len()) <= cfg.SetMaxIntset.Load() {
			return "intset"
		}
		if int64(x.Len()) <= cfg.SetMaxEntries.Load() {
			small := true
			x.ForEach(func(m string) bool {
				if int64(len(m)) > cfg.SetMaxValue.Load() {
					small = false
					return false
				}
				return true
			})
			if small {
				return "listpack"
			}
		}
		return "hashtable"
	case *ds.ZSet:
		if int64(x.Len()) <= cfg.ZSetMaxEntries.Load() {
			small := true
			x.ForEach(func(m string, _ float64) bool {
				if int64(len(m)) > cfg.ZSetMaxValue.Load() {
					small = false
					return false
				}
				return true
			})
			if small {
				return "listpack"
			}
		}
		return "skiplist"
	case *ds.Stream:
		return "stream"
	}
	return "unknown"
}

func cmdObject(c *Ctx) {
	sub := strings.ToLower(c.Arg(1))
	if sub == "help" {
		c.writeHelp("OBJECT", []string{
			"ENCODING <key>", "    Return the kind of internal representation used in order to store the value",
			"    associated with a <key>.",
			"FREQ <key>", "    Return the access frequency index of the <key>. The returned integer is",
			"    proportional to the logarithm of the recent access frequency of the key.",
			"IDLETIME <key>", "    Return the idle time of the <key>, that is the approximated number of",
			"    seconds elapsed since the last access to the key.",
			"REFCOUNT <key>", "    Return the number of references of the value associated with the specified",
			"    <key>.",
		})
		return
	}
	if len(c.Args) != 3 {
		if sub == "encoding" || sub == "freq" || sub == "idletime" || sub == "refcount" {
			c.WrongSubArgs()
		} else {
			c.UnknownSub()
		}
		return
	}
	c.DB.NoTouch = true
	e := c.DB.Lookup(c.Arg(2))
	switch sub {
	case "encoding", "freq", "idletime", "refcount":
	default:
		c.UnknownSub()
		return
	}
	if e == nil {
		c.W.WriteNull()
		return
	}
	switch sub {
	case "encoding":
		c.W.WriteBulkString(c.s.objectEncoding(e.Value))
	case "refcount":
		if b, ok := e.Value.([]byte); ok {
			if n, isInt := parseInt(b); isInt && n >= 0 && n < 10000 {
				c.W.WriteInt(2147483647)
				return
			}
		}
		c.W.WriteInt(1)
	case "idletime":
		p := c.s.cfg.MaxMemoryPolicy.Load()
		if p == config.PolicyAllKeysLFU || p == config.PolicyVolatileLFU {
			c.Err("ERR An LFU maxmemory policy is selected, idle time not tracked. Please note that when switching between policies at runtime LRU and LFU data will take some time to adjust.")
			return
		}
		c.W.WriteInt(int64(c.s.ks.Clock.Load() - e.LRU()))
	case "freq":
		p := c.s.cfg.MaxMemoryPolicy.Load()
		if p != config.PolicyAllKeysLFU && p != config.PolicyVolatileLFU {
			c.Err("ERR An LFU maxmemory policy is not selected, access frequency not tracked. Please note that when switching between policies at runtime LRU and LFU data will take some time to adjust.")
			return
		}
		c.W.WriteInt(int64(c.s.ks.LFUCounter(e)))
	}
}

func cmdDump(c *Ctx) {
	e := c.Lookup(c.Arg(1))
	if e == nil {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(storage.EncodeValue(e.Value))
}

func cmdRestore(c *Ctx) {
	key := c.Arg(1)
	ttl, ok := c.Int(2)
	if !ok {
		return
	}
	var replace, absttl bool
	var idle, freq int64 = -1, -1
	for i := 4; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "replace"):
			replace = true
		case c.Is(i, "absttl"):
			absttl = true
		case c.Is(i, "idletime") && i+1 < len(c.Args) && freq == -1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				c.Err("ERR Invalid IDLETIME value, must be >= 0")
				return
			}
			idle = n
			i++
		case c.Is(i, "freq") && i+1 < len(c.Args) && idle == -1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 || n > 255 {
				c.Err("ERR Invalid FREQ value, must be >= 0 and <= 255")
				return
			}
			freq = n
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	if ttl < 0 {
		c.Err("ERR Invalid TTL value, must be >= 0")
		return
	}
	if !replace && c.DB.Lookup(key) != nil {
		c.Err("BUSYKEY Target key name already exists.")
		return
	}
	v, err := storage.DecodeValue(c.Args[3])
	if err != nil {
		c.Err("ERR DUMP payload version or checksum are wrong")
		return
	}
	at := int64(0)
	if ttl > 0 {
		if absttl {
			at = ttl
		} else {
			at = time.Now().UnixMilli() + ttl
		}
	}
	if at != 0 && at <= time.Now().UnixMilli() && !c.isMaster {
		if c.DB.Delete(key) {
			c.Dirty(1)
			c.RewriteStrings("DEL", key)
		}
		c.W.WriteOK()
		return
	}
	e := c.s.ks.NewEntry(v)
	e.ExpireAt = at
	if idle >= 0 {
		e.SetLRU(c.s.ks.Clock.Load() - uint32(idle))
	}
	if freq >= 0 {
		e.SetLFU(uint32(time.Now().Unix()/60)&65535<<8 | uint32(freq))
	}
	c.DB.Set(key, e)
	c.Dirty(1)
	if at != 0 {
		args := [][]byte{[]byte("RESTORE"), c.Args[1], formatInt(at), c.Args[3], []byte("REPLACE"), []byte("ABSTTL")}
		c.Rewrite(args...)
	}
	c.W.WriteOK()
}

func cmdDBSize(c *Ctx) { c.W.WriteInt(c.s.ks.DBSize(c.DB.Idx)) }

func parseFlushMode(c *Ctx, from int) bool {
	if len(c.Args) > from+1 {
		c.SyntaxErr()
		return false
	}
	if len(c.Args) == from+1 && !c.Is(from, "async") && !c.Is(from, "sync") {
		c.SyntaxErr()
		return false
	}
	return true
}

func cmdFlushDB(c *Ctx) {
	if !parseFlushMode(c, 1) {
		return
	}
	c.s.ks.FlushDB(c.DB.Idx)
	c.s.watches.touchDB(c.DB.Idx)
	c.Dirty(1)
	c.W.WriteOK()
}

func cmdFlushAll(c *Ctx) {
	if !parseFlushMode(c, 1) {
		return
	}
	c.s.ks.FlushAll()
	c.s.watches.touchDB(-1)
	c.Dirty(1)
	c.W.WriteOK()
}

func cmdSwapDB(c *Ctx) {
	a, ok := c.parseDBIndexMsg(1, "ERR invalid first DB index")
	if !ok {
		return
	}
	b, ok := c.parseDBIndexMsg(2, "ERR invalid second DB index")
	if !ok {
		return
	}
	if a != b {
		c.s.ks.SwapDB(a, b)
		c.s.watches.touchDB(a)
		c.s.watches.touchDB(b)
		c.s.blocker.signalAll()
	}
	c.Dirty(1)
	c.W.WriteOK()
}

func (c *Ctx) parseDBIndexMsg(i int, msg string) (int, bool) {
	n, ok := parseInt(c.Args[i])
	if !ok {
		c.Err(msg)
		return 0, false
	}
	if n < 0 || n >= int64(c.s.ks.NumDB) {
		c.Err(errDBIndex)
		return 0, false
	}
	return int(n), true
}

func cmdSelect(c *Ctx) {
	n, ok := parseInt(c.Args[1])
	if !ok {
		c.Err("ERR invalid DB index")
		return
	}
	if n < 0 || n >= int64(c.s.ks.NumDB) {
		c.Err(errDBIndex)
		return
	}
	if c.inScript {
		c.scriptSelect(int(n))
	} else if c.cl != nil {
		c.cl.db = int(n)
		c.cl.dbAtomic.Store(int32(n))
	}
	c.DB.Idx = int(n)
	c.W.WriteOK()
}

// ---- SORT ----

type sortItem struct {
	val   []byte
	score float64
	cmp   []byte
}

func sortKeys(args [][]byte) ([]int, bool) {
	pos := []int{1}
	lockAll := false
	for i := 2; i < len(args); i++ {
		a := strings.ToLower(string(args[i]))
		switch a {
		case "by", "get":
			if i+1 < len(args) {
				p := string(args[i+1])
				if a == "get" && p == "#" {
					i++
					continue
				}
				if strings.Contains(p, "*") {
					lockAll = true
				}
				i++
			}
		case "limit":
			i += 2
		case "store":
			if i+1 < len(args) {
				pos = append(pos, i+1)
				i++
			}
		}
	}
	return pos, lockAll
}

// lookupPattern resolves a SORT BY/GET pattern for element subst.
func (c *Ctx) lookupPattern(pattern string, subst []byte) ([]byte, bool) {
	if pattern == "#" {
		return subst, true
	}
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return nil, false
	}
	field := ""
	keyPat := pattern
	if arrow := strings.Index(pattern, "->"); arrow > star && arrow+2 < len(pattern) {
		field = pattern[arrow+2:]
		keyPat = pattern[:arrow]
	}
	key := keyPat[:star] + string(subst) + keyPat[star+1:]
	e := c.DB.Lookup(key)
	if e == nil {
		return nil, false
	}
	if field == "" {
		b, ok := e.Value.([]byte)
		return b, ok
	}
	h, ok := e.Value.(*ds.Hash)
	if !ok {
		return nil, false
	}
	return h.Get(field, c.DB.Now)
}

func cmdSort(c *Ctx)   { sortGeneric(c, false) }
func cmdSortRO(c *Ctx) { sortGeneric(c, true) }

func sortGeneric(c *Ctx, ro bool) {
	key := c.Arg(1)
	desc, alpha := false, false
	var limitStart, limitCount int64 = 0, -1
	var byPat string
	dontSort := false
	var gets []string
	var store string
	for i := 2; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case c.Is(i, "asc"):
			desc = false
		case c.Is(i, "desc"):
			desc = true
		case c.Is(i, "alpha"):
			alpha = true
		case c.Is(i, "limit") && left >= 2:
			a, ok := c.Int(i + 1)
			if !ok {
				return
			}
			b, ok := c.Int(i + 2)
			if !ok {
				return
			}
			limitStart, limitCount = a, b
			i += 2
		case c.Is(i, "store") && left >= 1 && !ro:
			store = c.Arg(i + 1)
			i++
		case c.Is(i, "by") && left >= 1:
			byPat = c.Arg(i + 1)
			if !strings.Contains(byPat, "*") {
				dontSort = true
			}
			i++
		case c.Is(i, "get") && left >= 1:
			gets = append(gets, c.Arg(i+1))
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	e := c.Lookup(key)
	var items []sortItem
	if e != nil {
		switch v := e.Value.(type) {
		case *ds.List:
			for i := 0; i < v.Len(); i++ {
				items = append(items, sortItem{val: v.Index(i)})
			}
		case *ds.Set:
			v.ForEach(func(m string) bool { items = append(items, sortItem{val: []byte(m)}); return true })
			if dontSort && (store != "" || c.inScript) {
				// Deterministic output for replication/scripts.
				dontSort = false
				alpha = true
			}
		case *ds.ZSet:
			v.ForEach(func(m string, _ float64) bool { items = append(items, sortItem{val: []byte(m)}); return true })
		default:
			c.WrongType()
			return
		}
	}
	// ZSET with no sort: keep natural order but honour DESC.
	isZSet := false
	if e != nil {
		_, isZSet = e.Value.(*ds.ZSet)
	}
	if !dontSort {
		for i := range items {
			var cmpVal []byte
			if byPat != "" {
				v, ok := c.lookupPattern(byPat, items[i].val)
				if ok {
					cmpVal = v
				}
			} else {
				cmpVal = items[i].val
			}
			if alpha {
				items[i].cmp = cmpVal
			} else if cmpVal != nil {
				f, ok := parseFloat(cmpVal)
				if !ok {
					c.Err("ERR One or more scores can't be converted into double")
					return
				}
				items[i].score = f
			}
		}
		sort.SliceStable(items, func(i, j int) bool {
			var r int
			if alpha {
				r = strings.Compare(string(items[i].cmp), string(items[j].cmp))
			} else {
				switch {
				case items[i].score < items[j].score:
					r = -1
				case items[i].score > items[j].score:
					r = 1
				}
			}
			if r == 0 {
				r = strings.Compare(string(items[i].val), string(items[j].val))
			}
			if desc {
				return r > 0
			}
			return r < 0
		})
	} else if isZSet && desc {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	// LIMIT
	start, count := limitStart, limitCount
	n := int64(len(items))
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end := n
	if count >= 0 && start+count < n {
		end = start + count
	}
	items = items[start:end]
	var out [][]byte
	var outNull []bool
	for _, it := range items {
		if len(gets) == 0 {
			out = append(out, it.val)
			outNull = append(outNull, false)
			continue
		}
		for _, g := range gets {
			v, ok := c.lookupPattern(g, it.val)
			out = append(out, v)
			outNull = append(outNull, !ok)
		}
	}
	if store != "" {
		if len(out) == 0 {
			if c.DB.Delete(store) {
				c.Dirty(1)
			}
			c.Dirty(1)
		} else {
			l := ds.NewList()
			for _, v := range out {
				if v == nil {
					v = []byte{}
				}
				l.PushBack(append([]byte(nil), v...))
			}
			c.DB.SetValue(store, l, false)
			c.Dirty(int64(len(out)))
		}
		c.W.WriteInt(int64(len(out)))
		return
	}
	c.W.WriteArrayLen(len(out))
	for i, v := range out {
		if outNull[i] {
			c.W.WriteNull()
		} else {
			c.W.WriteBulk(v)
		}
	}
}
