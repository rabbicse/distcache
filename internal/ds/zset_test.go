package ds

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"
)

type zpair struct {
	m string
	s float64
}

func zsetMembers(z *ZSet) []string {
	var out []string
	z.ForEach(func(m string, _ float64) bool { out = append(out, m); return true })
	return out
}

func sortedPairs(ps []zpair) []zpair {
	out := slices.Clone(ps)
	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s < out[j].s
		}
		return out[i].m < out[j].m
	})
	return out
}

func TestZSet_Rank_MatchesSortedIndex(t *testing.T) {
	z := NewZSet()
	var ps []zpair
	for i := range 1000 {
		p := zpair{fmt.Sprintf("m%04d", i), float64(rand.IntN(200))} // many ties
		ps = append(ps, p)
		z.Add(p.m, p.s)
	}
	want := sortedPairs(ps)
	for i, p := range want {
		r, ok := z.Rank(p.m, false)
		if !ok || r != i {
			t.Fatalf("Rank(%s) got %d,%v, want %d", p.m, r, ok, i)
		}
		rr, _ := z.Rank(p.m, true)
		if rr != len(want)-1-i {
			t.Fatalf("reverse Rank(%s) got %d, want %d", p.m, rr, len(want)-1-i)
		}
		if n := z.ByRank(i); n == nil || n.Member != p.m || n.Score != p.s {
			t.Fatalf("ByRank(%d) got %+v, want %v", i, n, p)
		}
	}
	if z.ByRank(-1) != nil || z.ByRank(1000) != nil {
		t.Error("ByRank out of range should be nil")
	}
	if _, ok := z.Rank("nope", false); ok {
		t.Error("Rank of missing member reported ok")
	}
	if z.First().Member != want[0].m || z.Last().Member != want[len(want)-1].m {
		t.Error("First/Last mismatch")
	}
	// Backward links must walk the same order in reverse.
	i := len(want) - 1
	for n := z.Last(); n != nil; n = n.Prev() {
		if n.Member != want[i].m {
			t.Fatalf("Prev walk at %d got %s, want %s", i, n.Member, want[i].m)
		}
		i--
	}
}

func TestZSet_TiesOrderedByMember(t *testing.T) {
	z := NewZSet()
	for _, m := range []string{"d", "a", "c", "b"} {
		z.Add(m, 1)
	}
	if got, want := zsetMembers(z), []string{"a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestZSet_AddUpdateRem(t *testing.T) {
	z := NewZSet()
	if added, changed := z.Add("a", 1); !added || !changed {
		t.Errorf("first add got %v,%v", added, changed)
	}
	if added, changed := z.Add("a", 1); added || changed {
		t.Errorf("same score got %v,%v", added, changed)
	}
	if added, changed := z.Add("a", 5); added || !changed {
		t.Errorf("update got %v,%v", added, changed)
	}
	z.Add("b", 3)
	if s, ok := z.Score("a"); !ok || s != 5 {
		t.Errorf("Score got %v,%v", s, ok)
	}
	if got := zsetMembers(z); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("order after update got %v", got)
	}
	if !z.Rem("a") || z.Rem("a") {
		t.Error("Rem should report true then false")
	}
	if z.Len() != 1 || z.Bytes() <= 0 {
		t.Errorf("Len/Bytes got %d/%d", z.Len(), z.Bytes())
	}
	z.Rem("b")
	if z.Bytes() != 0 || z.First() != nil || z.Last() != nil {
		t.Error("empty zset should have no nodes and zero bytes")
	}
}

func TestZSet_ScoreRange(t *testing.T) {
	z := NewZSet()
	for i := 1; i <= 10; i++ {
		z.Add(fmt.Sprintf("m%02d", i), float64(i))
	}
	tests := []struct {
		name        string
		r           ScoreRange
		first, last string
		count       int
	}{
		{"inclusive", ScoreRange{Min: 2, Max: 5}, "m02", "m05", 4},
		{"exclusive min (1", ScoreRange{Min: 1, MinEx: true, Max: 3}, "m02", "m03", 2},
		{"exclusive both", ScoreRange{Min: 2, MinEx: true, Max: 5, MaxEx: true}, "m03", "m04", 2},
		{"-inf..+inf", ScoreRange{Min: math.Inf(-1), Max: math.Inf(1)}, "m01", "m10", 10},
		{"empty equal exclusive", ScoreRange{Min: 3, Max: 3, MaxEx: true}, "", "", 0},
		{"below all", ScoreRange{Min: -5, Max: 0}, "", "", 0},
		{"above all", ScoreRange{Min: 11, Max: 20}, "", "", 0},
		{"gap", ScoreRange{Min: 5.2, Max: 5.8}, "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, l := z.FirstInScore(&tc.r), z.LastInScore(&tc.r)
			if tc.count == 0 {
				if f != nil || l != nil {
					t.Fatalf("expected nil nodes, got %v %v", f, l)
				}
			} else if f == nil || l == nil || f.Member != tc.first || l.Member != tc.last {
				t.Fatalf("got %v..%v, want %s..%s", f, l, tc.first, tc.last)
			}
			if got := z.CountInScore(&tc.r); got != tc.count {
				t.Errorf("count got %d, want %d", got, tc.count)
			}
		})
	}
	r := ScoreRange{Min: 1, MinEx: true, Max: 3}
	if r.Contains(1) || !r.Contains(3) || r.Contains(3.1) {
		t.Error("Contains wrong")
	}
	if !(&ScoreRange{Min: 5, Max: 1}).Empty() {
		t.Error("inverted range should be empty")
	}
}

func TestZSet_RemoveByScoreRange(t *testing.T) {
	z := NewZSet()
	for i := 1; i <= 10; i++ {
		z.Add(fmt.Sprintf("m%02d", i), float64(i))
	}
	r := ScoreRange{Min: 3, Max: 6, MaxEx: true}
	var doomed []string
	for n := z.FirstInScore(&r); n != nil && r.Contains(n.Score); n = n.Next() {
		doomed = append(doomed, n.Member)
	}
	for _, m := range doomed {
		z.Rem(m)
	}
	want := []string{"m01", "m02", "m06", "m07", "m08", "m09", "m10"}
	if got := zsetMembers(z); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func lexZSet() *ZSet {
	z := NewZSet()
	for _, m := range []string{"a", "b", "c", "d", "e", "f"} {
		z.Add(m, 0)
	}
	return z
}

func TestZSet_LexRange(t *testing.T) {
	minf, pinf := LexBound{Inf: -1}, LexBound{Inf: 1}
	inc := func(v string) LexBound { return LexBound{Val: v} }
	exc := func(v string) LexBound { return LexBound{Val: v, Ex: true} }
	tests := []struct {
		name        string
		r           LexRange
		first, last string
		count       int
	}{
		{"- +", LexRange{minf, pinf}, "a", "f", 6},
		{"[b [d", LexRange{inc("b"), inc("d")}, "b", "d", 3},
		{"(b (d", LexRange{exc("b"), exc("d")}, "c", "c", 1},
		{"[b +", LexRange{inc("b"), pinf}, "b", "f", 5},
		{"- (c", LexRange{minf, exc("c")}, "a", "b", 2},
		{"(f +", LexRange{exc("f"), pinf}, "", "", 0},
		{"[x [z", LexRange{inc("x"), inc("z")}, "", "", 0},
		{"inverted", LexRange{inc("d"), inc("b")}, "", "", 0},
		{"+ -", LexRange{pinf, minf}, "", "", 0},
		{"(c [c", LexRange{exc("c"), inc("c")}, "", "", 0},
	}
	z := lexZSet()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, l := z.FirstInLex(&tc.r), z.LastInLex(&tc.r)
			if tc.count == 0 {
				if f != nil || l != nil {
					t.Fatalf("expected nil, got %v %v", f, l)
				}
			} else if f == nil || l == nil || f.Member != tc.first || l.Member != tc.last {
				t.Fatalf("got %v..%v, want %s..%s", f, l, tc.first, tc.last)
			}
			if got := z.CountInLex(&tc.r); got != tc.count {
				t.Errorf("count got %d, want %d", got, tc.count)
			}
		})
	}
	r := LexRange{inc("b"), exc("d")}
	if !r.Contains("b") || r.Contains("d") || r.Contains("a") {
		t.Error("LexRange.Contains wrong")
	}
}

func TestZSet_ForEach_EarlyStop(t *testing.T) {
	z := lexZSet()
	n := 0
	z.ForEach(func(string, float64) bool { n++; return n < 2 })
	if n != 2 {
		t.Errorf("iterations got %d, want 2", n)
	}
}

func TestZSet_RandomOps_ConsistentWithModel(t *testing.T) {
	z := NewZSet()
	model := map[string]float64{}
	for i := range 5000 {
		m := fmt.Sprintf("k%d", rand.IntN(300))
		if rand.IntN(3) == 0 {
			z.Rem(m)
			delete(model, m)
		} else {
			s := float64(rand.IntN(50))
			z.Add(m, s)
			model[m] = s
		}
		if i%500 == 0 && z.Len() != len(model) {
			t.Fatalf("Len got %d, want %d", z.Len(), len(model))
		}
	}
	var ps []zpair
	for m, s := range model {
		ps = append(ps, zpair{m, s})
	}
	for i, p := range sortedPairs(ps) {
		if r, _ := z.Rank(p.m, false); r != i {
			t.Fatalf("Rank(%s) got %d, want %d", p.m, r, i)
		}
	}
}

func TestZSet_Clone_IsDeep(t *testing.T) {
	z := NewZSet()
	for i := range 50 {
		z.Add(fmt.Sprint(i), float64(i))
	}
	c := z.Clone()
	z.Rem("0")
	z.Add("new", -1)
	if c.Len() != 50 {
		t.Fatalf("clone Len got %d, want 50", c.Len())
	}
	if _, ok := c.Score("new"); ok {
		t.Error("clone sees later insert")
	}
	if r, _ := c.Rank("0", false); r != 0 {
		t.Errorf("clone rank of 0 got %d, want 0", r)
	}
}

func TestIsNaN(t *testing.T) {
	if !IsNaN(math.NaN()) || IsNaN(1) {
		t.Error("IsNaN wrong")
	}
}

func TestRandomLevel_Bounds(t *testing.T) {
	for range 10000 {
		if l := randomLevel(); l < 1 || l > zslMaxLevel {
			t.Fatalf("level %d out of [1,%d]", l, zslMaxLevel)
		}
	}
}
