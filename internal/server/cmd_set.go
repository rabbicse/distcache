package server

import (
	"math"
	"math/rand/v2"
	"sort"

	"github.com/rabbicse/distcache/internal/ds"
)

func (c *Ctx) getSet(key string) (*ds.Set, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, true
	}
	s, ok := e.Value.(*ds.Set)
	if !ok {
		c.WrongType()
		return nil, false
	}
	return s, true
}

func cmdSAdd(c *Ctx) {
	key := c.Arg(1)
	s, ok := c.getSet(key)
	if !ok {
		return
	}
	if s == nil {
		s = ds.NewSet()
		c.DB.Add(key, s)
	}
	var n int64
	for _, m := range c.Args[2:] {
		if s.Add(string(m)) {
			n++
		}
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdSRem(c *Ctx) {
	key := c.Arg(1)
	s, ok := c.getSet(key)
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteInt(0)
		return
	}
	var n int64
	for _, m := range c.Args[2:] {
		if s.Rem(string(m)) {
			n++
		}
	}
	if s.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdSCard(c *Ctx) {
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(s.Len()))
}

func cmdSIsMember(c *Ctx) {
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	if s != nil && s.Has(c.Arg(2)) {
		c.W.WriteInt(1)
	} else {
		c.W.WriteInt(0)
	}
}

func cmdSMIsMember(c *Ctx) {
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(len(c.Args) - 2)
	for _, m := range c.Args[2:] {
		if s != nil && s.Has(string(m)) {
			c.W.WriteInt(1)
		} else {
			c.W.WriteInt(0)
		}
	}
}

func (c *Ctx) writeSet(members []string) {
	c.W.WriteSetLen(len(members))
	for _, m := range members {
		c.W.WriteBulkString(m)
	}
}

func cmdSMembers(c *Ctx) {
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteSetLen(0)
		return
	}
	c.W.WriteSetLen(s.Len())
	s.ForEach(func(m string) bool { c.W.WriteBulkString(m); return true })
}

func randomMember(s *ds.Set) string {
	// Go map iteration starts at a random position.
	var pick string
	skip := rand.IntN(minInt(s.Len(), 8))
	s.ForEach(func(m string) bool {
		pick = m
		if skip == 0 {
			return false
		}
		skip--
		return true
	})
	return pick
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func cmdSRandMember(c *Ctx) {
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	if len(c.Args) == 2 {
		if s == nil {
			c.W.WriteNull()
			return
		}
		c.W.WriteBulkString(randomMember(s))
		return
	}
	if len(c.Args) > 3 {
		c.SyntaxErr()
		return
	}
	count, ok := c.Int(2)
	if !ok {
		return
	}
	if count < -math.MaxInt64/2 || count > math.MaxInt64/2 {
		c.Err("ERR value is out of range")
		return
	}
	if s == nil || count == 0 {
		c.W.WriteArrayLen(0)
		return
	}
	members := s.Members()
	if count < 0 {
		c.W.WriteArrayLen(int(-count))
		for i := int64(0); i < -count; i++ {
			c.W.WriteBulkString(members[rand.IntN(len(members))])
		}
		return
	}
	if count >= int64(len(members)) {
		c.W.WriteArrayLen(len(members))
		for _, m := range members {
			c.W.WriteBulkString(m)
		}
		return
	}
	perm := rand.Perm(len(members))[:count]
	c.W.WriteArrayLen(len(perm))
	for _, i := range perm {
		c.W.WriteBulkString(members[i])
	}
}

func cmdSPop(c *Ctx) {
	key := c.Arg(1)
	if len(c.Args) > 3 {
		c.SyntaxErr()
		return
	}
	hasCount := len(c.Args) == 3
	var count int64 = 1
	if hasCount {
		n, ok := parseInt(c.Args[2])
		if !ok || n < 0 {
			c.Err(errCountNeg)
			return
		}
		count = n
	}
	s, ok := c.getSet(key)
	if !ok {
		return
	}
	if s == nil {
		if hasCount {
			c.W.WriteSetLen(0)
		} else {
			c.W.WriteNull()
		}
		return
	}
	if !hasCount {
		m := randomMember(s)
		s.Rem(m)
		if s.Len() == 0 {
			c.DB.Delete(key)
		}
		c.Dirty(1)
		c.RewriteStrings("SREM", key, m)
		c.W.WriteBulkString(m)
		return
	}
	if count == 0 {
		c.W.WriteSetLen(0)
		return
	}
	var popped []string
	if count >= int64(s.Len()) {
		popped = s.Members()
		c.DB.Delete(key)
		c.RewriteStrings("DEL", key)
	} else {
		members := s.Members()
		perm := rand.Perm(len(members))[:count]
		args := []string{"SREM", key}
		for _, i := range perm {
			popped = append(popped, members[i])
			s.Rem(members[i])
			args = append(args, members[i])
		}
		c.RewriteStrings(args...)
	}
	c.Dirty(int64(len(popped)))
	c.writeSet(popped)
}

func cmdSMove(c *Ctx) {
	src, dst, member := c.Arg(1), c.Arg(2), c.Arg(3)
	ss, ok := c.getSet(src)
	if !ok {
		return
	}
	ds2, ok := c.getSet(dst)
	if !ok {
		return
	}
	if ss == nil || !ss.Has(member) {
		c.W.WriteInt(0)
		return
	}
	if src == dst {
		c.W.WriteInt(1)
		return
	}
	ss.Rem(member)
	if ss.Len() == 0 {
		c.DB.Delete(src)
	}
	if ds2 == nil {
		ds2 = ds.NewSet()
		c.DB.Add(dst, ds2)
	}
	ds2.Add(member)
	c.Dirty(1)
	c.W.WriteInt(1)
}

// collectSets loads sets for keys; missing keys yield nil entries.
func (c *Ctx) collectSets(keys []string) ([]*ds.Set, bool) {
	out := make([]*ds.Set, len(keys))
	for i, k := range keys {
		s, ok := c.getSet(k)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

func setInter(sets []*ds.Set, limit int64) []string {
	for _, s := range sets {
		if s == nil || s.Len() == 0 {
			return nil
		}
	}
	sorted := append([]*ds.Set(nil), sets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Len() < sorted[j].Len() })
	var out []string
	sorted[0].ForEach(func(m string) bool {
		for _, o := range sorted[1:] {
			if !o.Has(m) {
				return true
			}
		}
		out = append(out, m)
		return limit <= 0 || int64(len(out)) < limit
	})
	return out
}

func setUnion(sets []*ds.Set) []string {
	acc := map[string]struct{}{}
	for _, s := range sets {
		if s == nil {
			continue
		}
		s.ForEach(func(m string) bool { acc[m] = struct{}{}; return true })
	}
	out := make([]string, 0, len(acc))
	for m := range acc {
		out = append(out, m)
	}
	return out
}

func setDiff(sets []*ds.Set) []string {
	if sets[0] == nil {
		return nil
	}
	var out []string
	sets[0].ForEach(func(m string) bool {
		for _, o := range sets[1:] {
			if o != nil && o.Has(m) {
				return true
			}
		}
		out = append(out, m)
		return true
	})
	return out
}

func argStrings(args [][]byte) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = string(a)
	}
	return out
}

func setOpGeneric(c *Ctx, op int, store bool) {
	keysStart := 1
	if store {
		keysStart = 2
	}
	sets, ok := c.collectSets(argStrings(c.Args[keysStart:]))
	if !ok {
		return
	}
	var res []string
	switch op {
	case 0:
		res = setInter(sets, 0)
	case 1:
		res = setUnion(sets)
	default:
		res = setDiff(sets)
	}
	if !store {
		c.writeSet(res)
		return
	}
	dst := c.Arg(1)
	if len(res) == 0 {
		if c.DB.Delete(dst) {
			c.Dirty(1)
		}
		c.W.WriteInt(0)
		return
	}
	ns := ds.NewSet()
	for _, m := range res {
		ns.Add(m)
	}
	c.DB.SetValue(dst, ns, false)
	c.Dirty(1)
	c.W.WriteInt(int64(len(res)))
}

func cmdSInter(c *Ctx)      { setOpGeneric(c, 0, false) }
func cmdSUnion(c *Ctx)      { setOpGeneric(c, 1, false) }
func cmdSDiff(c *Ctx)       { setOpGeneric(c, 2, false) }
func cmdSInterStore(c *Ctx) { setOpGeneric(c, 0, true) }
func cmdSUnionStore(c *Ctx) { setOpGeneric(c, 1, true) }
func cmdSDiffStore(c *Ctx)  { setOpGeneric(c, 2, true) }

func cmdSInterCard(c *Ctx) {
	n, ok := parseInt(c.Args[1])
	if !ok || n <= 0 {
		c.Err("ERR numkeys should be greater than 0")
		return
	}
	if n > int64(len(c.Args)-2) {
		c.Err("ERR Number of keys can't be greater than number of args")
		return
	}
	var limit int64
	rest := c.Args[2+n:]
	if len(rest) > 0 {
		if len(rest) != 2 || !c.Is(2+int(n), "limit") {
			c.SyntaxErr()
			return
		}
		l, ok := parseInt(rest[1])
		if !ok {
			c.Err(errNotInt)
			return
		}
		if l < 0 {
			c.Err("ERR LIMIT can't be negative")
			return
		}
		limit = l
	}
	sets, ok := c.collectSets(argStrings(c.Args[2 : 2+n]))
	if !ok {
		return
	}
	c.W.WriteInt(int64(len(setInter(sets, limit))))
}

func cmdSScan(c *Ctx) {
	if _, ok := parseCursor(c.Args[2]); !ok {
		c.Err("ERR invalid cursor")
		return
	}
	pattern, _, ok := parseScanOpts(c, 3, false)
	if !ok {
		return
	}
	s, ok := c.getSet(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(2)
	c.W.WriteBulkString("0")
	if s == nil {
		c.W.WriteArrayLen(0)
		return
	}
	var out []string
	s.ForEach(func(m string) bool {
		if pattern == "" || ds.Match(pattern, m, false) {
			out = append(out, m)
		}
		return true
	})
	c.W.WriteStrings(out)
}
