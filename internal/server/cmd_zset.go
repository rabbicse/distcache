package server

import (
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

func (c *Ctx) getZSet(key string) (*ds.ZSet, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, true
	}
	z, ok := e.Value.(*ds.ZSet)
	if !ok {
		c.WrongType()
		return nil, false
	}
	return z, true
}

func parseScoreBound(b []byte) (float64, bool, bool) {
	ex := false
	if len(b) > 0 && b[0] == '(' {
		ex = true
		b = b[1:]
	}
	f, ok := parseFloatAllowNaN(b)
	if !ok || math.IsNaN(f) {
		return 0, false, false
	}
	return f, ex, true
}

func parseFloatAllowNaN(b []byte) (float64, bool) { return parseFloat(b) }

func (c *Ctx) parseScoreRange(minB, maxB []byte) (*ds.ScoreRange, bool) {
	min, minEx, ok1 := parseScoreBound(minB)
	max, maxEx, ok2 := parseScoreBound(maxB)
	if !ok1 || !ok2 {
		c.Err(errMinMaxFloat)
		return nil, false
	}
	return &ds.ScoreRange{Min: min, Max: max, MinEx: minEx, MaxEx: maxEx}, true
}

func parseLexBound(b []byte) (ds.LexBound, bool) {
	if len(b) == 0 {
		return ds.LexBound{}, false
	}
	switch b[0] {
	case '+':
		if len(b) != 1 {
			return ds.LexBound{}, false
		}
		return ds.LexBound{Inf: 1}, true
	case '-':
		if len(b) != 1 {
			return ds.LexBound{}, false
		}
		return ds.LexBound{Inf: -1}, true
	case '(':
		return ds.LexBound{Val: string(b[1:]), Ex: true}, true
	case '[':
		return ds.LexBound{Val: string(b[1:])}, true
	}
	return ds.LexBound{}, false
}

func (c *Ctx) parseLexRange(minB, maxB []byte) (*ds.LexRange, bool) {
	min, ok1 := parseLexBound(minB)
	max, ok2 := parseLexBound(maxB)
	if !ok1 || !ok2 {
		c.Err(errLexRange)
		return nil, false
	}
	return &ds.LexRange{Min: min, Max: max}, true
}

func cmdZAdd(c *Ctx) {
	key := c.Arg(1)
	var nx, xx, gt, lt, ch, incr bool
	i := 2
loop:
	for ; i < len(c.Args); i++ {
		switch strings.ToLower(c.Arg(i)) {
		case "nx":
			nx = true
		case "xx":
			xx = true
		case "gt":
			gt = true
		case "lt":
			lt = true
		case "ch":
			ch = true
		case "incr":
			incr = true
		default:
			break loop
		}
	}
	rest := c.Args[i:]
	if len(rest) == 0 || len(rest)%2 != 0 {
		c.SyntaxErr()
		return
	}
	if nx && xx {
		c.Err("ERR XX and NX options at the same time are not compatible")
		return
	}
	if (gt && nx) || (lt && nx) || (gt && lt) {
		c.Err("ERR GT, LT, and/or NX options at the same time are not compatible")
		return
	}
	if incr && len(rest) > 2 {
		c.Err("ERR INCR option supports a single increment-element pair")
		return
	}
	scores := make([]float64, len(rest)/2)
	for j := range scores {
		f, ok := parseFloat(rest[2*j])
		if !ok {
			c.Err(errNotFloat)
			return
		}
		scores[j] = f
	}
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil {
		if xx {
			if incr {
				c.W.WriteNull()
			} else {
				c.W.WriteInt(0)
			}
			return
		}
		z = ds.NewZSet()
		c.DB.Add(key, z)
	}
	var added, changed int64
	var lastScore float64
	aborted := false
	for j := range scores {
		member := string(rest[2*j+1])
		score := scores[j]
		cur, exists := z.Score(member)
		if exists {
			if nx {
				aborted = true
				continue
			}
			if incr {
				score += cur
				if math.IsNaN(score) {
					c.Err(errNaN)
					if z.Len() == 0 {
						c.DB.Delete(key)
					}
					return
				}
			}
			if (gt && score <= cur) || (lt && score >= cur) {
				aborted = true
				continue
			}
			lastScore = score
			if score != cur {
				z.Add(member, score)
				changed++
			}
		} else {
			if xx {
				aborted = true
				continue
			}
			z.Add(member, score)
			added++
			lastScore = score
		}
	}
	if z.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(added + changed)
	if incr {
		if aborted {
			c.W.WriteNull()
		} else {
			c.W.WriteDouble(lastScore)
		}
		if added+changed > 0 {
			c.Rewrite([]byte("ZADD"), c.Args[1], []byte(resp.FormatDouble(lastScore)), rest[1])
		}
		return
	}
	if ch {
		c.W.WriteInt(added + changed)
	} else {
		c.W.WriteInt(added)
	}
}

func cmdZIncrBy(c *Ctx) {
	incr, ok := parseFloat(c.Args[2])
	if !ok {
		c.Err(errNotFloat)
		return
	}
	key, member := c.Arg(1), c.Arg(3)
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil {
		z = ds.NewZSet()
		c.DB.Add(key, z)
	}
	cur, _ := z.Score(member)
	score := cur + incr
	if math.IsNaN(score) {
		c.Err(errNaN)
		if z.Len() == 0 {
			c.DB.Delete(key)
		}
		return
	}
	z.Add(member, score)
	c.Dirty(1)
	c.Rewrite([]byte("ZADD"), c.Args[1], []byte(resp.FormatDouble(score)), c.Args[3])
	c.W.WriteDouble(score)
}

func cmdZRem(c *Ctx) {
	key := c.Arg(1)
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteInt(0)
		return
	}
	var n int64
	for _, m := range c.Args[2:] {
		if z.Rem(string(m)) {
			n++
		}
	}
	if z.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdZCard(c *Ctx) {
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(z.Len()))
}

func cmdZScore(c *Ctx) {
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteNull()
		return
	}
	s, exists := z.Score(c.Arg(2))
	if !exists {
		c.W.WriteNull()
		return
	}
	c.W.WriteDouble(s)
}

func cmdZMScore(c *Ctx) {
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
		if s, exists := z.Score(string(m)); exists {
			c.W.WriteDouble(s)
		} else {
			c.W.WriteNull()
		}
	}
}

func zrankGeneric(c *Ctx, reverse bool) {
	withScore := false
	if len(c.Args) == 4 {
		if !c.Is(3, "withscore") {
			c.SyntaxErr()
			return
		}
		withScore = true
	} else if len(c.Args) > 4 {
		c.SyntaxErr()
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		if withScore {
			c.W.WriteNullArray()
		} else {
			c.W.WriteNull()
		}
		return
	}
	r, exists := z.Rank(c.Arg(2), reverse)
	if !exists {
		if withScore {
			c.W.WriteNullArray()
		} else {
			c.W.WriteNull()
		}
		return
	}
	if withScore {
		s, _ := z.Score(c.Arg(2))
		c.W.WriteArrayLen(2)
		c.W.WriteInt(int64(r))
		c.W.WriteDouble(s)
		return
	}
	c.W.WriteInt(int64(r))
}

func cmdZRank(c *Ctx)    { zrankGeneric(c, false) }
func cmdZRevRank(c *Ctx) { zrankGeneric(c, true) }

func cmdZCount(c *Ctx) {
	r, ok := c.parseScoreRange(c.Args[2], c.Args[3])
	if !ok {
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(z.CountInScore(r)))
}

func cmdZLexCount(c *Ctx) {
	r, ok := c.parseLexRange(c.Args[2], c.Args[3])
	if !ok {
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(z.CountInLex(r)))
}

// zrangeSpec describes a generic ZRANGE request.
type zrangeSpec struct {
	by         int // 0 rank, 1 score, 2 lex
	rev        bool
	withScores bool
	offset     int64
	limit      int64 // -1 = all
	minArg     []byte
	maxArg     []byte
	limitSet   bool
}

type zmember struct {
	member string
	score  float64
}

// zrangeCollect evaluates a range spec against z.
func (c *Ctx) zrangeCollect(z *ds.ZSet, sp *zrangeSpec) ([]zmember, bool) {
	var out []zmember
	switch sp.by {
	case 0:
		start, ok1 := parseInt(sp.minArg)
		end, ok2 := parseInt(sp.maxArg)
		if !ok1 || !ok2 {
			c.Err(errNotInt)
			return nil, false
		}
		if z == nil {
			return nil, true
		}
		n := int64(z.Len())
		s, e, ok := normRange(start, end, n)
		if !ok {
			return nil, true
		}
		if sp.rev {
			node := z.ByRank(int(n - 1 - s))
			for i := s; i <= e && node != nil; i++ {
				out = append(out, zmember{node.Member, node.Score})
				node = node.Prev()
			}
		} else {
			node := z.ByRank(int(s))
			for i := s; i <= e && node != nil; i++ {
				out = append(out, zmember{node.Member, node.Score})
				node = node.Next()
			}
		}
		return out, true
	case 1:
		minB, maxB := sp.minArg, sp.maxArg
		if sp.rev {
			minB, maxB = maxB, minB
		}
		r, ok := c.parseScoreRange(minB, maxB)
		if !ok {
			return nil, false
		}
		if z == nil {
			return nil, true
		}
		var node *ds.ZNode
		if sp.rev {
			node = z.LastInScore(r)
		} else {
			node = z.FirstInScore(r)
		}
		off := sp.offset
		for node != nil && off > 0 {
			off--
			if sp.rev {
				node = node.Prev()
			} else {
				node = node.Next()
			}
		}
		for node != nil && r.Contains(node.Score) && (sp.limit < 0 || int64(len(out)) < sp.limit) {
			out = append(out, zmember{node.Member, node.Score})
			if sp.rev {
				node = node.Prev()
			} else {
				node = node.Next()
			}
		}
		return out, true
	default:
		minB, maxB := sp.minArg, sp.maxArg
		if sp.rev {
			minB, maxB = maxB, minB
		}
		r, ok := c.parseLexRange(minB, maxB)
		if !ok {
			return nil, false
		}
		if z == nil {
			return nil, true
		}
		var node *ds.ZNode
		if sp.rev {
			node = z.LastInLex(r)
		} else {
			node = z.FirstInLex(r)
		}
		off := sp.offset
		for node != nil && off > 0 {
			off--
			if sp.rev {
				node = node.Prev()
			} else {
				node = node.Next()
			}
		}
		for node != nil && r.Contains(node.Member) && (sp.limit < 0 || int64(len(out)) < sp.limit) {
			out = append(out, zmember{node.Member, node.Score})
			if sp.rev {
				node = node.Prev()
			} else {
				node = node.Next()
			}
		}
		return out, true
	}
}

func writeZMembers(w *resp.Writer, ms []zmember, withScores bool) {
	if withScores && w.Proto >= 3 {
		w.WriteArrayLen(len(ms))
		for _, m := range ms {
			w.WriteArrayLen(2)
			w.WriteBulkString(m.member)
			w.WriteDouble(m.score)
		}
		return
	}
	if withScores {
		w.WriteArrayLen(2 * len(ms))
	} else {
		w.WriteArrayLen(len(ms))
	}
	for _, m := range ms {
		w.WriteBulkString(m.member)
		if withScores {
			w.WriteDouble(m.score)
		}
	}
}

// parseZRangeOpts parses ZRANGE style options from position i.
func (c *Ctx) parseZRangeOpts(sp *zrangeSpec, i int, allowWithScores, allowBy bool) bool {
	for ; i < len(c.Args); i++ {
		switch {
		case allowWithScores && c.Is(i, "withscores"):
			sp.withScores = true
		case c.Is(i, "limit") && i+2 < len(c.Args):
			off, ok := c.Int(i + 1)
			if !ok {
				return false
			}
			lim, ok := c.Int(i + 2)
			if !ok {
				return false
			}
			sp.offset, sp.limit = off, lim
			i += 2
			sp.limitSet = true
		case allowBy && c.Is(i, "byscore"):
			sp.by = 1
		case allowBy && c.Is(i, "bylex"):
			sp.by = 2
		case allowBy && c.Is(i, "rev"):
			sp.rev = true
		default:
			c.SyntaxErr()
			return false
		}
	}
	return true
}

func zrangeValidate(c *Ctx, sp *zrangeSpec) bool {
	if sp.limitSet && sp.by == 0 {
		c.Err("ERR syntax error, LIMIT is only supported in combination with either BYSCORE or BYLEX")
		return false
	}
	if sp.withScores && sp.by == 2 {
		c.Err("ERR syntax error, WITHSCORES not supported in combination with BYLEX")
		return false
	}
	if sp.offset < 0 {
		// Negative offset returns an empty list in Redis.
		sp.limit = 0
		sp.offset = 0
	}
	return true
}

func cmdZRange(c *Ctx) {
	sp := &zrangeSpec{limit: -1, minArg: c.Args[2], maxArg: c.Args[3]}
	if !c.parseZRangeOpts(sp, 4, true, true) || !zrangeValidate(c, sp) {
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	ms, ok := c.zrangeCollect(z, sp)
	if !ok {
		return
	}
	writeZMembers(c.W, ms, sp.withScores)
}

func legacyZRange(c *Ctx, by int, rev bool) {
	sp := &zrangeSpec{by: by, rev: rev, limit: -1, minArg: c.Args[2], maxArg: c.Args[3]}
	if !c.parseZRangeOpts(sp, 4, by != 2, false) || !zrangeValidate(c, sp) {
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	ms, ok := c.zrangeCollect(z, sp)
	if !ok {
		return
	}
	writeZMembers(c.W, ms, sp.withScores)
}

func cmdZRevRange(c *Ctx) {
	if len(c.Args) > 5 || (len(c.Args) == 5 && !c.Is(4, "withscores")) {
		c.SyntaxErr()
		return
	}
	legacyZRange(c, 0, true)
}
func cmdZRangeByScore(c *Ctx)    { legacyZRange(c, 1, false) }
func cmdZRevRangeByScore(c *Ctx) { legacyZRange(c, 1, true) }
func cmdZRangeByLex(c *Ctx)      { legacyZRange(c, 2, false) }
func cmdZRevRangeByLex(c *Ctx)   { legacyZRange(c, 2, true) }

func cmdZRangeStore(c *Ctx) {
	sp := &zrangeSpec{limit: -1, minArg: c.Args[3], maxArg: c.Args[4]}
	if !c.parseZRangeOpts(sp, 5, false, true) || !zrangeValidate(c, sp) {
		return
	}
	z, ok := c.getZSet(c.Arg(2))
	if !ok {
		return
	}
	ms, ok := c.zrangeCollect(z, sp)
	if !ok {
		return
	}
	dst := c.Arg(1)
	if len(ms) == 0 {
		if c.DB.Delete(dst) {
			c.Dirty(1)
		}
		c.W.WriteInt(0)
		return
	}
	nz := ds.NewZSet()
	for _, m := range ms {
		nz.Add(m.member, m.score)
	}
	c.DB.SetValue(dst, nz, false)
	c.Dirty(1)
	c.W.WriteInt(int64(len(ms)))
}

func zremRangeGeneric(c *Ctx, by int) {
	key := c.Arg(1)
	sp := &zrangeSpec{by: by, limit: -1, minArg: c.Args[2], maxArg: c.Args[3]}
	// Validate the range before the type check like Redis.
	switch by {
	case 1:
		if _, ok := c.parseScoreRange(c.Args[2], c.Args[3]); !ok {
			return
		}
	case 2:
		if _, ok := c.parseLexRange(c.Args[2], c.Args[3]); !ok {
			return
		}
	default:
		if _, ok := c.Int(2); !ok {
			return
		}
		if _, ok := c.Int(3); !ok {
			return
		}
	}
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil {
		c.W.WriteInt(0)
		return
	}
	ms, ok := c.zrangeCollect(z, sp)
	if !ok {
		return
	}
	for _, m := range ms {
		z.Rem(m.member)
	}
	if z.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(int64(len(ms)))
	c.W.WriteInt(int64(len(ms)))
}

func cmdZRemRangeByRank(c *Ctx)  { zremRangeGeneric(c, 0) }
func cmdZRemRangeByScore(c *Ctx) { zremRangeGeneric(c, 1) }
func cmdZRemRangeByLex(c *Ctx)   { zremRangeGeneric(c, 2) }

// zpop pops up to count members from z.
func zpop(z *ds.ZSet, max bool, count int64) []zmember {
	var out []zmember
	for i := int64(0); i < count && z.Len() > 0; i++ {
		var n *ds.ZNode
		if max {
			n = z.Last()
		} else {
			n = z.First()
		}
		out = append(out, zmember{n.Member, n.Score})
		z.Rem(n.Member)
	}
	return out
}

func zpopGeneric(c *Ctx, max bool) {
	key := c.Arg(1)
	count := int64(-1)
	if len(c.Args) > 3 {
		c.SyntaxErr()
		return
	}
	if len(c.Args) == 3 {
		n, ok := parseInt(c.Args[2])
		if !ok || n < 0 {
			c.Err(errCountNeg)
			return
		}
		count = n
	}
	z, ok := c.getZSet(key)
	if !ok {
		return
	}
	if z == nil || count == 0 {
		c.W.WriteArrayLen(0)
		return
	}
	nested := len(c.Args) == 3 && c.W.Proto >= 3
	if count == -1 {
		count = 1
	}
	ms := zpop(z, max, count)
	if z.Len() == 0 {
		c.DB.Delete(key)
	}
	c.Dirty(int64(len(ms)))
	args := []string{"ZREM", key}
	for _, m := range ms {
		args = append(args, m.member)
	}
	c.RewriteStrings(args...)
	if nested {
		writeZMembers(c.W, ms, true)
		return
	}
	c.W.WriteArrayLen(2 * len(ms))
	for _, m := range ms {
		c.W.WriteBulkString(m.member)
		c.W.WriteDouble(m.score)
	}
}

func cmdZPopMin(c *Ctx) { zpopGeneric(c, false) }
func cmdZPopMax(c *Ctx) { zpopGeneric(c, true) }

func bzpopGeneric(c *Ctx, max bool) {
	deadline, ok := c.blockDeadline(len(c.Args)-1, false)
	if !ok {
		return
	}
	keys := c.Args[1 : len(c.Args)-1]
	for _, k := range keys {
		key := string(k)
		z, ok := c.getZSet(key)
		if !ok {
			return
		}
		if z == nil {
			continue
		}
		ms := zpop(z, max, 1)
		if z.Len() == 0 {
			c.DB.Delete(key)
		}
		c.Dirty(1)
		c.keys = []string{key}
		c.RewriteStrings("ZREM", key, ms[0].member)
		c.W.WriteArrayLen(3)
		c.W.WriteBulk(k)
		c.W.WriteBulkString(ms[0].member)
		c.W.WriteDouble(ms[0].score)
		return
	}
	if !c.canBlock {
		c.W.WriteNullArray()
		return
	}
	c.Block(argStrings(keys), deadline, nullArrayReply)
}

func cmdBZPopMin(c *Ctx) { bzpopGeneric(c, false) }
func cmdBZPopMax(c *Ctx) { bzpopGeneric(c, true) }

func zmpopCore(c *Ctx, keys []string, max bool, count int64) bool {
	for _, key := range keys {
		z, ok := c.getZSet(key)
		if !ok {
			return true
		}
		if z == nil {
			continue
		}
		ms := zpop(z, max, count)
		if z.Len() == 0 {
			c.DB.Delete(key)
		}
		c.Dirty(int64(len(ms)))
		c.keys = []string{key}
		args := []string{"ZREM", key}
		for _, m := range ms {
			args = append(args, m.member)
		}
		c.RewriteStrings(args...)
		c.W.WriteArrayLen(2)
		c.W.WriteBulkString(key)
		c.W.WriteArrayLen(len(ms))
		for _, m := range ms {
			c.W.WriteArrayLen(2)
			c.W.WriteBulkString(m.member)
			c.W.WriteDouble(m.score)
		}
		return true
	}
	return false
}

func cmdZMPop(c *Ctx) {
	keys, min, count, ok := mpopArgs(c, 1, [2]string{"min", "max"})
	if !ok {
		return
	}
	if !zmpopCore(c, keys, !min, count) {
		c.W.WriteNullArray()
	}
}

func cmdBZMPop(c *Ctx) {
	deadline, ok := c.blockDeadline(1, false)
	if !ok {
		return
	}
	keys, min, count, ok := mpopArgs(c, 2, [2]string{"min", "max"})
	if !ok {
		return
	}
	if zmpopCore(c, keys, !min, count) {
		return
	}
	if !c.canBlock {
		c.W.WriteNullArray()
		return
	}
	c.Block(keys, deadline, nullArrayReply)
}

func cmdZRandMember(c *Ctx) {
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	if len(c.Args) == 2 {
		if z == nil {
			c.W.WriteNull()
			return
		}
		n := z.ByRank(rand.IntN(z.Len()))
		c.W.WriteBulkString(n.Member)
		return
	}
	count, ok := c.Int(2)
	if !ok {
		return
	}
	withScores := false
	if len(c.Args) == 4 {
		if !c.Is(3, "withscores") {
			c.SyntaxErr()
			return
		}
		withScores = true
	} else if len(c.Args) > 4 {
		c.SyntaxErr()
		return
	}
	if count < -math.MaxInt64/2 || count > math.MaxInt64/2 {
		c.Err("ERR value is out of range")
		return
	}
	if z == nil || count == 0 {
		c.W.WriteArrayLen(0)
		return
	}
	var ms []zmember
	if count < 0 {
		for i := int64(0); i < -count; i++ {
			n := z.ByRank(rand.IntN(z.Len()))
			ms = append(ms, zmember{n.Member, n.Score})
		}
	} else {
		perm := rand.Perm(z.Len())
		if count < int64(len(perm)) {
			perm = perm[:count]
		}
		for _, r := range perm {
			n := z.ByRank(r)
			ms = append(ms, zmember{n.Member, n.Score})
		}
	}
	writeZMembers(c.W, ms, withScores)
}

// ---- ZUNION / ZINTER / ZDIFF ----

func (c *Ctx) zsetOrSetScores(key string) (map[string]float64, bool, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, false, true
	}
	switch v := e.Value.(type) {
	case *ds.ZSet:
		m := make(map[string]float64, v.Len())
		v.ForEach(func(mem string, s float64) bool { m[mem] = s; return true })
		return m, true, true
	case *ds.Set:
		m := make(map[string]float64, v.Len())
		v.ForEach(func(mem string) bool { m[mem] = 1; return true })
		return m, true, true
	}
	c.WrongType()
	return nil, false, false
}

func zaggregate(agg int, a, b float64) float64 {
	switch agg {
	case 1:
		return math.Min(a, b)
	case 2:
		return math.Max(a, b)
	}
	r := a + b
	if math.IsNaN(r) {
		return 0
	}
	return r
}

// zsetOp: op 0 union, 1 inter, 2 diff. dstPos>0 means *STORE.
func zsetOpGeneric(c *Ctx, op int, store bool) {
	numPos := 1
	if store {
		numPos = 2
	}
	n, ok := parseInt(c.Args[numPos])
	if !ok {
		c.Err(errNotInt)
		return
	}
	if n < 1 {
		c.Err("ERR at least 1 input key is needed for '" + c.Cmd.Name + "' command")
		return
	}
	if n > int64(len(c.Args)-numPos-1) {
		c.SyntaxErr()
		return
	}
	keys := argStrings(c.Args[numPos+1 : numPos+1+int(n)])
	weights := make([]float64, n)
	for i := range weights {
		weights[i] = 1
	}
	agg := 0
	withScores := false
	for i := numPos + 1 + int(n); i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case op != 2 && c.Is(i, "weights") && left >= int(n):
			for j := 0; j < int(n); j++ {
				w, ok := parseFloat(c.Args[i+1+j])
				if !ok {
					c.Err("ERR weight value is not a float")
					return
				}
				weights[j] = w
			}
			i += int(n)
		case op != 2 && c.Is(i, "aggregate") && left >= 1:
			switch strings.ToLower(c.Arg(i + 1)) {
			case "sum":
				agg = 0
			case "min":
				agg = 1
			case "max":
				agg = 2
			default:
				c.SyntaxErr()
				return
			}
			i++
		case !store && c.Is(i, "withscores"):
			withScores = true
		default:
			c.SyntaxErr()
			return
		}
	}
	inputs := make([]map[string]float64, n)
	for i, k := range keys {
		m, _, ok := c.zsetOrSetScores(k)
		if !ok {
			return
		}
		inputs[i] = m
	}
	res := map[string]float64{}
	switch op {
	case 0:
		for i, m := range inputs {
			for mem, s := range m {
				ws := s * weights[i]
				if math.IsNaN(ws) {
					ws = 0
				}
				if cur, ok := res[mem]; ok {
					res[mem] = zaggregate(agg, cur, ws)
				} else {
					res[mem] = ws
				}
			}
		}
	case 1:
		if len(inputs) > 0 && inputs[0] != nil {
			for mem, s := range inputs[0] {
				acc := s * weights[0]
				if math.IsNaN(acc) {
					acc = 0
				}
				in := true
				for i := 1; i < len(inputs); i++ {
					o, ok := inputs[i][mem]
					if !ok {
						in = false
						break
					}
					ws := o * weights[i]
					if math.IsNaN(ws) {
						ws = 0
					}
					acc = zaggregate(agg, acc, ws)
				}
				if in {
					res[mem] = acc
				}
			}
		}
	case 2:
		if inputs[0] != nil {
			for mem, s := range inputs[0] {
				in := true
				for i := 1; i < len(inputs); i++ {
					if _, ok := inputs[i][mem]; ok {
						in = false
						break
					}
				}
				if in {
					res[mem] = s
				}
			}
		}
	}
	ms := make([]zmember, 0, len(res))
	for mem, s := range res {
		ms = append(ms, zmember{mem, s})
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].score != ms[j].score {
			return ms[i].score < ms[j].score
		}
		return ms[i].member < ms[j].member
	})
	if !store {
		writeZMembers(c.W, ms, withScores)
		return
	}
	dst := c.Arg(1)
	if len(ms) == 0 {
		if c.DB.Delete(dst) {
			c.Dirty(1)
		}
		c.W.WriteInt(0)
		return
	}
	nz := ds.NewZSet()
	for _, m := range ms {
		nz.Add(m.member, m.score)
	}
	c.DB.SetValue(dst, nz, false)
	c.Dirty(1)
	c.W.WriteInt(int64(len(ms)))
}

func cmdZUnion(c *Ctx)      { zsetOpGeneric(c, 0, false) }
func cmdZInter(c *Ctx)      { zsetOpGeneric(c, 1, false) }
func cmdZDiff(c *Ctx)       { zsetOpGeneric(c, 2, false) }
func cmdZUnionStore(c *Ctx) { zsetOpGeneric(c, 0, true) }
func cmdZInterStore(c *Ctx) { zsetOpGeneric(c, 1, true) }
func cmdZDiffStore(c *Ctx)  { zsetOpGeneric(c, 2, true) }

func cmdZInterCard(c *Ctx) {
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
	var inputs []map[string]float64
	for _, k := range argStrings(c.Args[2 : 2+n]) {
		m, _, ok := c.zsetOrSetScores(k)
		if !ok {
			return
		}
		inputs = append(inputs, m)
	}
	count := int64(0)
	if inputs[0] != nil {
		for mem := range inputs[0] {
			in := true
			for _, o := range inputs[1:] {
				if _, ok := o[mem]; !ok {
					in = false
					break
				}
			}
			if in {
				count++
				if limit > 0 && count >= limit {
					break
				}
			}
		}
	}
	c.W.WriteInt(count)
}

func cmdZScan(c *Ctx) {
	if _, ok := parseCursor(c.Args[2]); !ok {
		c.Err("ERR invalid cursor")
		return
	}
	pattern, _, ok := parseScanOpts(c, 3, false)
	if !ok {
		return
	}
	z, ok := c.getZSet(c.Arg(1))
	if !ok {
		return
	}
	c.W.WriteArrayLen(2)
	c.W.WriteBulkString("0")
	if z == nil {
		c.W.WriteArrayLen(0)
		return
	}
	var out []zmember
	z.ForEach(func(m string, s float64) bool {
		if pattern == "" || ds.Match(pattern, m, false) {
			out = append(out, zmember{m, s})
		}
		return true
	})
	c.W.WriteArrayLen(2 * len(out))
	for _, m := range out {
		c.W.WriteBulkString(m.member)
		c.W.WriteBulkString(resp.FormatDouble(m.score))
	}
}

// zsetStoreKeys: dst at 1, numkeys at 2.
func zstoreKeys(args [][]byte) ([]int, bool) {
	pos := []int{1}
	if len(args) > 2 {
		n, ok := parseInt(args[2])
		if ok {
			for j := 0; j < int(n) && 3+j < len(args); j++ {
				pos = append(pos, 3+j)
			}
		}
	}
	return pos, false
}
