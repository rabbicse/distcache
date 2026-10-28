package ds

import (
	"math"
	"math/rand/v2"
)

// Sorted set implemented exactly like Redis: a hash map (member -> score)
// plus a skiplist ordered by (score, member) with span counters for O(log n)
// rank queries.

const (
	zslMaxLevel = 32
	zslP        = 0.25
	// ZSetMemberOverhead is the estimated per-member overhead.
	ZSetMemberOverhead = 80
)

type zlevel struct {
	forward *ZNode
	span    int
}

// ZNode is a skiplist node.
type ZNode struct {
	Member   string
	Score    float64
	backward *ZNode
	level    []zlevel
}

// Next returns the following node in score order.
func (n *ZNode) Next() *ZNode { return n.level[0].forward }

// Prev returns the previous node in score order.
func (n *ZNode) Prev() *ZNode { return n.backward }

type skiplist struct {
	header, tail *ZNode
	length       int
	level        int
}

func newSkiplist() *skiplist {
	return &skiplist{header: &ZNode{level: make([]zlevel, zslMaxLevel)}, level: 1}
}

func randomLevel() int {
	l := 1
	for l < zslMaxLevel && rand.Float64() < zslP {
		l++
	}
	return l
}

func less(s1 float64, m1 string, s2 float64, m2 string) bool {
	return s1 < s2 || (s1 == s2 && m1 < m2)
}

func (z *skiplist) insert(score float64, member string) *ZNode {
	var update [zslMaxLevel]*ZNode
	var rank [zslMaxLevel]int
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		if i == z.level-1 {
			rank[i] = 0
		} else {
			rank[i] = rank[i+1]
		}
		for x.level[i].forward != nil && less(x.level[i].forward.Score, x.level[i].forward.Member, score, member) {
			rank[i] += x.level[i].span
			x = x.level[i].forward
		}
		update[i] = x
	}
	lvl := randomLevel()
	if lvl > z.level {
		for i := z.level; i < lvl; i++ {
			rank[i] = 0
			update[i] = z.header
			update[i].level[i].span = z.length
		}
		z.level = lvl
	}
	x = &ZNode{Member: member, Score: score, level: make([]zlevel, lvl)}
	for i := 0; i < lvl; i++ {
		x.level[i].forward = update[i].level[i].forward
		update[i].level[i].forward = x
		x.level[i].span = update[i].level[i].span - (rank[0] - rank[i])
		update[i].level[i].span = (rank[0] - rank[i]) + 1
	}
	for i := lvl; i < z.level; i++ {
		update[i].level[i].span++
	}
	if update[0] != z.header {
		x.backward = update[0]
	}
	if x.level[0].forward != nil {
		x.level[0].forward.backward = x
	} else {
		z.tail = x
	}
	z.length++
	return x
}

func (z *skiplist) deleteNode(x *ZNode, update *[zslMaxLevel]*ZNode) {
	for i := 0; i < z.level; i++ {
		if update[i].level[i].forward == x {
			update[i].level[i].span += x.level[i].span - 1
			update[i].level[i].forward = x.level[i].forward
		} else {
			update[i].level[i].span--
		}
	}
	if x.level[0].forward != nil {
		x.level[0].forward.backward = x.backward
	} else {
		z.tail = x.backward
	}
	for z.level > 1 && z.header.level[z.level-1].forward == nil {
		z.level--
	}
	z.length--
}

func (z *skiplist) delete(score float64, member string) bool {
	var update [zslMaxLevel]*ZNode
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && less(x.level[i].forward.Score, x.level[i].forward.Member, score, member) {
			x = x.level[i].forward
		}
		update[i] = x
	}
	x = x.level[0].forward
	if x != nil && x.Score == score && x.Member == member {
		z.deleteNode(x, &update)
		return true
	}
	return false
}

// rank returns the 1-based rank of (score, member), 0 if missing.
func (z *skiplist) rank(score float64, member string) int {
	r := 0
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && (x.level[i].forward.Score < score ||
			(x.level[i].forward.Score == score && x.level[i].forward.Member <= member)) {
			r += x.level[i].span
			x = x.level[i].forward
		}
		if x != z.header && x.Member == member {
			return r
		}
	}
	return 0
}

// byRank returns the node with 1-based rank r.
func (z *skiplist) byRank(r int) *ZNode {
	traversed := 0
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && traversed+x.level[i].span <= r {
			traversed += x.level[i].span
			x = x.level[i].forward
		}
		if traversed == r {
			return x
		}
	}
	return nil
}

// ScoreRange describes a score interval.
type ScoreRange struct {
	Min, Max     float64
	MinEx, MaxEx bool
}

func (r *ScoreRange) gteMin(v float64) bool {
	if r.MinEx {
		return v > r.Min
	}
	return v >= r.Min
}
func (r *ScoreRange) lteMax(v float64) bool {
	if r.MaxEx {
		return v < r.Max
	}
	return v <= r.Max
}

// Contains reports whether v is in range.
func (r *ScoreRange) Contains(v float64) bool { return r.gteMin(v) && r.lteMax(v) }

// Empty reports whether the range can never match.
func (r *ScoreRange) Empty() bool {
	return r.Min > r.Max || (r.Min == r.Max && (r.MinEx || r.MaxEx))
}

// LexBound is one end of a lexicographic range.
type LexBound struct {
	Inf int // -1 => '-', +1 => '+', 0 => value
	Val string
	Ex  bool
}

// LexRange describes a lex interval.
type LexRange struct{ Min, Max LexBound }

func lexGteMin(v string, b LexBound) bool {
	switch b.Inf {
	case -1:
		return true
	case 1:
		return false
	}
	if b.Ex {
		return v > b.Val
	}
	return v >= b.Val
}

func lexLteMax(v string, b LexBound) bool {
	switch b.Inf {
	case 1:
		return true
	case -1:
		return false
	}
	if b.Ex {
		return v < b.Val
	}
	return v <= b.Val
}

// Contains reports whether v is inside the lex range.
func (r *LexRange) Contains(v string) bool { return lexGteMin(v, r.Min) && lexLteMax(v, r.Max) }

// Empty reports whether the lex range can never match.
func (r *LexRange) Empty() bool {
	a, b := r.Min, r.Max
	if a.Inf == 1 || b.Inf == -1 {
		return true
	}
	if a.Inf == -1 || b.Inf == 1 {
		return false
	}
	if a.Val > b.Val {
		return true
	}
	return a.Val == b.Val && (a.Ex || b.Ex)
}

func (z *skiplist) firstInScore(r *ScoreRange) *ZNode {
	if r.Empty() || z.tail == nil || !r.gteMin(z.tail.Score) {
		return nil
	}
	if x := z.header.level[0].forward; x == nil || !r.lteMax(x.Score) {
		return nil
	}
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && !r.gteMin(x.level[i].forward.Score) {
			x = x.level[i].forward
		}
	}
	x = x.level[0].forward
	if x == nil || !r.lteMax(x.Score) {
		return nil
	}
	return x
}

func (z *skiplist) lastInScore(r *ScoreRange) *ZNode {
	if r.Empty() || z.tail == nil || !r.gteMin(z.tail.Score) {
		return nil
	}
	if x := z.header.level[0].forward; x == nil || !r.lteMax(x.Score) {
		return nil
	}
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && r.lteMax(x.level[i].forward.Score) {
			x = x.level[i].forward
		}
	}
	if x == z.header || !r.gteMin(x.Score) {
		return nil
	}
	return x
}

func (z *skiplist) firstInLex(r *LexRange) *ZNode {
	if r.Empty() || z.tail == nil || !lexGteMin(z.tail.Member, r.Min) {
		return nil
	}
	if x := z.header.level[0].forward; x == nil || !lexLteMax(x.Member, r.Max) {
		return nil
	}
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && !lexGteMin(x.level[i].forward.Member, r.Min) {
			x = x.level[i].forward
		}
	}
	x = x.level[0].forward
	if x == nil || !lexLteMax(x.Member, r.Max) {
		return nil
	}
	return x
}

func (z *skiplist) lastInLex(r *LexRange) *ZNode {
	if r.Empty() || z.tail == nil || !lexGteMin(z.tail.Member, r.Min) {
		return nil
	}
	if x := z.header.level[0].forward; x == nil || !lexLteMax(x.Member, r.Max) {
		return nil
	}
	x := z.header
	for i := z.level - 1; i >= 0; i-- {
		for x.level[i].forward != nil && lexLteMax(x.level[i].forward.Member, r.Max) {
			x = x.level[i].forward
		}
	}
	if x == z.header || !lexGteMin(x.Member, r.Min) {
		return nil
	}
	return x
}

// ZSet is a Redis sorted set.
type ZSet struct {
	dict  map[string]float64
	zsl   *skiplist
	bytes int64
}

// NewZSet returns an empty sorted set.
func NewZSet() *ZSet { return &ZSet{dict: make(map[string]float64), zsl: newSkiplist()} }

// Len returns the cardinality.
func (z *ZSet) Len() int { return len(z.dict) }

// Bytes returns estimated memory usage.
func (z *ZSet) Bytes() int64 { return z.bytes + int64(len(z.dict))*ZSetMemberOverhead }

// Score returns the score of member.
func (z *ZSet) Score(member string) (float64, bool) {
	s, ok := z.dict[member]
	return s, ok
}

// Add sets member's score. Returns added (new member) and changed (score
// differs). NaN must be rejected by the caller.
func (z *ZSet) Add(member string, score float64) (added, changed bool) {
	cur, ok := z.dict[member]
	if ok {
		if cur == score {
			return false, false
		}
		z.zsl.delete(cur, member)
		z.zsl.insert(score, member)
		z.dict[member] = score
		return false, true
	}
	z.zsl.insert(score, member)
	z.dict[member] = score
	z.bytes += int64(len(member)) + 8
	return true, true
}

// Rem removes member; returns true if it existed.
func (z *ZSet) Rem(member string) bool {
	s, ok := z.dict[member]
	if !ok {
		return false
	}
	z.zsl.delete(s, member)
	delete(z.dict, member)
	z.bytes -= int64(len(member)) + 8
	return true
}

// Rank returns the 0-based rank of member.
func (z *ZSet) Rank(member string, reverse bool) (int, bool) {
	s, ok := z.dict[member]
	if !ok {
		return 0, false
	}
	r := z.zsl.rank(s, member)
	if reverse {
		return z.zsl.length - r, true
	}
	return r - 1, true
}

// ByRank returns the node at 0-based rank (ascending).
func (z *ZSet) ByRank(r int) *ZNode {
	if r < 0 || r >= z.zsl.length {
		return nil
	}
	return z.zsl.byRank(r + 1)
}

// First returns the lowest node.
func (z *ZSet) First() *ZNode { return z.zsl.header.level[0].forward }

// Last returns the highest node.
func (z *ZSet) Last() *ZNode { return z.zsl.tail }

// FirstInScore returns the first node inside r.
func (z *ZSet) FirstInScore(r *ScoreRange) *ZNode { return z.zsl.firstInScore(r) }

// LastInScore returns the last node inside r.
func (z *ZSet) LastInScore(r *ScoreRange) *ZNode { return z.zsl.lastInScore(r) }

// FirstInLex returns the first node inside r.
func (z *ZSet) FirstInLex(r *LexRange) *ZNode { return z.zsl.firstInLex(r) }

// LastInLex returns the last node inside r.
func (z *ZSet) LastInLex(r *LexRange) *ZNode { return z.zsl.lastInLex(r) }

// CountInScore counts members in r using ranks (O(log n)).
func (z *ZSet) CountInScore(r *ScoreRange) int {
	f := z.zsl.firstInScore(r)
	if f == nil {
		return 0
	}
	l := z.zsl.lastInScore(r)
	return z.zsl.rank(l.Score, l.Member) - z.zsl.rank(f.Score, f.Member) + 1
}

// CountInLex counts members in r.
func (z *ZSet) CountInLex(r *LexRange) int {
	f := z.zsl.firstInLex(r)
	if f == nil {
		return 0
	}
	l := z.zsl.lastInLex(r)
	return z.zsl.rank(l.Score, l.Member) - z.zsl.rank(f.Score, f.Member) + 1
}

// ForEach iterates in ascending order.
func (z *ZSet) ForEach(fn func(member string, score float64) bool) {
	for x := z.First(); x != nil; x = x.Next() {
		if !fn(x.Member, x.Score) {
			return
		}
	}
}

// Clone returns a deep copy.
func (z *ZSet) Clone() *ZSet {
	n := NewZSet()
	for x := z.First(); x != nil; x = x.Next() {
		n.Add(x.Member, x.Score)
	}
	return n
}

// IsNaN helper for callers.
func IsNaN(f float64) bool { return math.IsNaN(f) }
