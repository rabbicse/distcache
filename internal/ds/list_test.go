package ds

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
)

func listOf(vals ...string) *List {
	l := NewList()
	for _, v := range vals {
		l.PushBack([]byte(v))
	}
	return l
}

func listStrings(l *List) []string {
	out := make([]string, 0, l.Len())
	for _, b := range l.All() {
		out = append(out, string(b))
	}
	return out
}

func TestList_PushPop_BothEnds(t *testing.T) {
	l := NewList()
	if _, ok := l.PopFront(); ok {
		t.Fatal("PopFront on empty list reported ok")
	}
	if _, ok := l.PopBack(); ok {
		t.Fatal("PopBack on empty list reported ok")
	}
	l.PushBack([]byte("b"))
	l.PushBack([]byte("c"))
	l.PushFront([]byte("a"))
	if got, want := listStrings(l), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if v, _ := l.PopFront(); string(v) != "a" {
		t.Errorf("PopFront got %q, want a", v)
	}
	if v, _ := l.PopBack(); string(v) != "c" {
		t.Errorf("PopBack got %q, want c", v)
	}
	if l.Len() != 1 {
		t.Errorf("Len got %d, want 1", l.Len())
	}
}

func TestList_Index_NegativeNormalised(t *testing.T) {
	// Index takes a normalised non-negative index; callers translate -1 etc.
	l := listOf("a", "b", "c", "d")
	norm := func(i int) int {
		if i < 0 {
			return l.Len() + i
		}
		return i
	}
	tests := []struct {
		idx  int
		want string
	}{{0, "a"}, {-1, "d"}, {-4, "a"}, {2, "c"}}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.idx), func(t *testing.T) {
			if got := string(l.Index(norm(tc.idx))); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestList_Slice_Bounds(t *testing.T) {
	l := listOf("a", "b", "c", "d", "e")
	tests := []struct {
		name         string
		start, stop  int
		want         []string
		wantNilEmpty bool
	}{
		{"all", 0, 4, []string{"a", "b", "c", "d", "e"}, false},
		{"middle", 1, 3, []string{"b", "c", "d"}, false},
		{"single", 2, 2, []string{"c"}, false},
		{"inverted", 3, 1, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := l.Slice(tc.start, tc.stop)
			if tc.wantNilEmpty {
				if len(got) != 0 {
					t.Fatalf("got %v, want empty", got)
				}
				return
			}
			var s []string
			for _, b := range got {
				s = append(s, string(b))
			}
			if !slices.Equal(s, tc.want) {
				t.Errorf("got %v, want %v", s, tc.want)
			}
		})
	}
	if got := NewList().All(); len(got) != 0 {
		t.Errorf("All on empty got %v", got)
	}
}

func TestList_Set_UpdatesBytes(t *testing.T) {
	l := listOf("aa", "bb")
	before := l.Bytes()
	l.Set(1, []byte("bbbbbb"))
	if got := string(l.Index(1)); got != "bbbbbb" {
		t.Fatalf("got %q, want bbbbbb", got)
	}
	if got, want := l.Bytes()-before, int64(4); got != want {
		t.Errorf("Bytes delta got %d, want %d", got, want)
	}
}

func TestList_Trim(t *testing.T) {
	tests := []struct {
		name        string
		start, stop int
		want        []string
	}{
		{"keep middle", 1, 3, []string{"b", "c", "d"}},
		{"keep all", 0, 4, []string{"a", "b", "c", "d", "e"}},
		{"inverted empties", 3, 1, nil},
		{"start beyond end empties", 7, 9, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := listOf("a", "b", "c", "d", "e")
			l.Trim(tc.start, tc.stop)
			if got := listStrings(l); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// removeCount emulates LREM count semantics on top of Filter.
func removeCount(l *List, elem string, count int) int {
	limit := count
	if limit < 0 {
		limit = -limit
	}
	// Negative count removes from the tail: find the indexes first.
	var idxs []int
	for i := 0; i < l.Len(); i++ {
		if string(l.Index(i)) == elem {
			idxs = append(idxs, i)
		}
	}
	if count < 0 {
		slices.Reverse(idxs)
	}
	if count != 0 && len(idxs) > limit {
		idxs = idxs[:limit]
	}
	drop := map[int]bool{}
	for _, i := range idxs {
		drop[i] = true
	}
	return l.Filter(func(i int, _ []byte) bool { return !drop[i] })
}

func TestList_Filter_RemCountSemantics(t *testing.T) {
	tests := []struct {
		name  string
		in    []string
		count int
		want  []string
		n     int
	}{
		{"all", []string{"a", "b", "a", "c", "a"}, 0, []string{"b", "c"}, 3},
		{"head two", []string{"a", "b", "a", "a", "c"}, 2, []string{"b", "a", "c"}, 2},
		{"tail two", []string{"a", "b", "a", "c", "a"}, -2, []string{"a", "b", "c"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := listOf(tc.in...)
			if n := removeCount(l, "a", tc.count); n != tc.n {
				t.Errorf("removed got %d, want %d", n, tc.n)
			}
			if got := listStrings(l); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestList_Insert_BeforeAfterPivot(t *testing.T) {
	tests := []struct {
		name string
		idx  int
		want []string
	}{
		{"front", 0, []string{"x", "a", "b", "c"}},
		{"before pivot b", 1, []string{"a", "x", "b", "c"}},
		{"after pivot b", 2, []string{"a", "b", "x", "c"}},
		{"back", 3, []string{"a", "b", "c", "x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := listOf("a", "b", "c")
			before := l.Bytes()
			l.Insert(tc.idx, []byte("x"))
			if got := listStrings(l); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if l.Bytes() <= before {
				t.Errorf("Bytes did not grow: %d -> %d", before, l.Bytes())
			}
		})
	}
}

func TestList_RemoveAt_BothHalves(t *testing.T) {
	for _, i := range []int{0, 1, 3, 5, 6} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			vals := []string{"a", "b", "c", "d", "e", "f", "g"}
			l := listOf(vals...)
			l.RemoveAt(i)
			want := slices.Delete(slices.Clone(vals), i, i+1)
			if got := listStrings(l); !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestList_Bytes_GrowsAndShrinks(t *testing.T) {
	l := NewList()
	empty := l.Bytes()
	for i := range 1000 {
		l.PushBack(bytes.Repeat([]byte("x"), 10+i%3))
	}
	grown := l.Bytes()
	if grown <= empty {
		t.Fatalf("Bytes did not grow: %d -> %d", empty, grown)
	}
	for l.Len() > 4 {
		l.PopFront()
	}
	if shrunk := l.Bytes(); shrunk >= grown {
		t.Errorf("Bytes did not shrink: %d -> %d", grown, shrunk)
	}
}

func TestList_RingWrapAround(t *testing.T) {
	l := NewList()
	var want []string
	for i := range 200 {
		v := fmt.Sprint(i)
		if i%2 == 0 {
			l.PushFront([]byte(v))
			want = slices.Insert(want, 0, v)
		} else {
			l.PushBack([]byte(v))
			want = append(want, v)
		}
		if i%5 == 0 {
			l.PopFront()
			want = want[1:]
		}
	}
	if got := listStrings(l); !slices.Equal(got, want) {
		t.Fatalf("ring contents diverged from model")
	}
}

func TestList_Clone_IsDeep(t *testing.T) {
	l := listOf("abc", "def")
	c := l.Clone()
	l.Index(0)[0] = 'X'
	l.PushBack([]byte("new"))
	if got, want := listStrings(c), []string{"abc", "def"}; !slices.Equal(got, want) {
		t.Errorf("clone mutated: got %v, want %v", got, want)
	}
}
