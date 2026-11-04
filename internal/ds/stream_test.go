package ds

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"testing"
)

func fields(kv ...string) [][]byte {
	out := make([][]byte, len(kv))
	for i, s := range kv {
		out[i] = []byte(s)
	}
	return out
}

func streamWith(n int) *Stream {
	s := NewStream()
	for i := 1; i <= n; i++ {
		s.Append(StreamID{Ms: uint64(i), Seq: 0}, fields("f", fmt.Sprint(i)))
	}
	return s
}

func TestStreamID_Cmp(t *testing.T) {
	tests := []struct {
		a, b StreamID
		want int
	}{
		{StreamID{1, 0}, StreamID{1, 0}, 0},
		{StreamID{1, 5}, StreamID{1, 6}, -1},
		{StreamID{2, 0}, StreamID{1, 99}, 1},
		{StreamID{0, 9}, StreamID{1, 0}, -1},
	}
	for _, tc := range tests {
		t.Run(tc.a.String()+"_"+tc.b.String(), func(t *testing.T) {
			if got := tc.a.Cmp(tc.b); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestStreamID_IncrDecr(t *testing.T) {
	if id, ok := (StreamID{1, 2}).Incr(); !ok || id != (StreamID{1, 3}) {
		t.Errorf("Incr got %v,%v", id, ok)
	}
	if id, ok := (StreamID{1, math.MaxUint64}).Incr(); !ok || id != (StreamID{2, 0}) {
		t.Errorf("Incr carry got %v,%v", id, ok)
	}
	if _, ok := MaxStreamID.Incr(); ok {
		t.Error("Incr of max should overflow")
	}
	if id, ok := (StreamID{1, 0}).Decr(); !ok || id != (StreamID{0, math.MaxUint64}) {
		t.Errorf("Decr borrow got %v,%v", id, ok)
	}
	if id, ok := (StreamID{3, 4}).Decr(); !ok || id != (StreamID{3, 3}) {
		t.Errorf("Decr got %v,%v", id, ok)
	}
	if _, ok := (StreamID{}).Decr(); ok {
		t.Error("Decr of 0-0 should underflow")
	}
	if !(StreamID{}).IsZero() || (StreamID{0, 1}).IsZero() {
		t.Error("IsZero wrong")
	}
}

func TestParseStreamID(t *testing.T) {
	tests := []struct {
		in         string
		missingSeq uint64
		want       StreamID
		seqGiven   bool
		seqAuto    bool
		ok         bool
	}{
		{"10-5", 0, StreamID{10, 5}, true, false, true},
		{"10", 0, StreamID{10, 0}, false, false, true},
		{"10", math.MaxUint64, StreamID{10, math.MaxUint64}, false, false, true},
		{"10-*", 0, StreamID{10, 0}, true, true, true},
		{"", 0, StreamID{}, false, false, false},
		{"abc", 0, StreamID{}, false, false, false},
		{"1-x", 0, StreamID{}, false, false, false},
		{"-1", 0, StreamID{}, false, false, false},
		{"99999999999999999999-0", 0, StreamID{}, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			id, given, auto, ok := ParseStreamID(tc.in, tc.missingSeq)
			if ok != tc.ok {
				t.Fatalf("ok got %v, want %v", ok, tc.ok)
			}
			if ok && (id != tc.want || given != tc.seqGiven || auto != tc.seqAuto) {
				t.Errorf("got %v,%v,%v, want %v,%v,%v", id, given, auto, tc.want, tc.seqGiven, tc.seqAuto)
			}
		})
	}
	if _, _, _, ok := ParseStreamID(string(bytes.Repeat([]byte("1"), 128)), 0); ok {
		t.Error("over-long ID accepted")
	}
}

func TestStream_Append_TracksLastIDAndLen(t *testing.T) {
	s := NewStream()
	if s.Len() != 0 || s.FirstID() != (StreamID{}) {
		t.Fatal("new stream not empty")
	}
	// Fixed ms with auto-sequence is the command layer's job; verify the
	// resulting IDs stay strictly monotonic through Incr.
	last := StreamID{5, 0}
	for range 5 {
		s.Append(last, fields("a", "b"))
		next, _ := last.Incr()
		if next.Cmp(last) <= 0 {
			t.Fatalf("Incr not monotonic: %v -> %v", last, next)
		}
		last = next
	}
	if s.Len() != 5 || s.EntriesAdded != 5 || s.LastID != (StreamID{5, 4}) || s.FirstID() != (StreamID{5, 0}) {
		t.Errorf("got len=%d added=%d last=%v first=%v", s.Len(), s.EntriesAdded, s.LastID, s.FirstID())
	}
	if s.Bytes() <= 0 {
		t.Error("Bytes should be positive")
	}
}

func TestStream_SeekGetDelete(t *testing.T) {
	s := streamWith(5)
	if i := s.Seek(StreamID{3, 0}); i != 2 {
		t.Errorf("Seek exact got %d, want 2", i)
	}
	if i := s.Seek(StreamID{3, 1}); i != 3 {
		t.Errorf("Seek between got %d, want 3", i)
	}
	if i := s.Seek(StreamID{99, 0}); i != 5 {
		t.Errorf("Seek past end got %d, want 5", i)
	}
	if e, ok := s.Get(StreamID{4, 0}); !ok || string(e.Fields[1]) != "4" {
		t.Errorf("Get got %v,%v", e, ok)
	}
	if _, ok := s.Get(StreamID{4, 1}); ok {
		t.Error("Get of absent ID reported ok")
	}
	before := s.Bytes()
	if !s.Delete(StreamID{3, 0}) || s.Delete(StreamID{3, 0}) {
		t.Error("Delete should report true then false")
	}
	if s.Len() != 4 || s.MaxDeletedID != (StreamID{3, 0}) || s.Bytes() >= before {
		t.Errorf("after delete len=%d maxdel=%v", s.Len(), s.MaxDeletedID)
	}
	if s.LastID != (StreamID{5, 0}) {
		t.Errorf("LastID must survive delete, got %v", s.LastID)
	}
	s.Delete(StreamID{1, 0})
	if s.MaxDeletedID != (StreamID{3, 0}) {
		t.Errorf("MaxDeletedID must not decrease, got %v", s.MaxDeletedID)
	}
}

func TestStream_TrimHead(t *testing.T) {
	tests := []struct {
		name      string
		n         int
		wantLen   int
		wantFirst StreamID
	}{
		{"zero no-op", 0, 10, StreamID{1, 0}},
		{"negative no-op", -3, 10, StreamID{1, 0}},
		{"exact maxlen 7", 3, 7, StreamID{4, 0}},
		{"more than len clamps", 99, 0, StreamID{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := streamWith(10)
			s.TrimHead(tc.n)
			if s.Len() != tc.wantLen || s.FirstID() != tc.wantFirst {
				t.Errorf("got len=%d first=%v, want %d %v", s.Len(), s.FirstID(), tc.wantLen, tc.wantFirst)
			}
		})
	}
	// MINID semantics: trim everything below an ID using Seek.
	s := streamWith(10)
	s.TrimHead(s.Seek(StreamID{6, 0}))
	if s.Len() != 5 || s.FirstID() != (StreamID{6, 0}) {
		t.Errorf("MINID trim got len=%d first=%v", s.Len(), s.FirstID())
	}
	if s.EntriesAdded != 10 || s.LastID != (StreamID{10, 0}) {
		t.Errorf("trim altered counters: %d %v", s.EntriesAdded, s.LastID)
	}
	if s.Bytes() <= 0 {
		t.Errorf("Bytes got %d", s.Bytes())
	}
}

func TestStream_Groups_PendingLifecycle(t *testing.T) {
	s := streamWith(5)
	g, ok := s.CreateGroup("g", StreamID{}, 0)
	if !ok {
		t.Fatal("CreateGroup failed")
	}
	if _, ok := s.CreateGroup("g", StreamID{}, 0); ok {
		t.Error("duplicate group created")
	}
	s.CreateGroup("a", StreamID{}, InvalidEntriesRead)
	if got := s.GroupNames(); !slices.Equal(got, []string{"a", "g"}) {
		t.Errorf("GroupNames got %v", got)
	}

	if c, _ := g.Consumer("none", 1, false); c != nil {
		t.Error("lookup without create returned a consumer")
	}
	c1, created := g.Consumer("c1", 100, true)
	if !created || c1.ActiveTime != -1 {
		t.Errorf("c1 created=%v active=%d", created, c1.ActiveTime)
	}
	if again, created := g.Consumer("c1", 200, true); created || again != c1 {
		t.Error("existing consumer should be returned, not recreated")
	}
	c2, _ := g.Consumer("c2", 100, true)
	if got := g.ConsumerNames(); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Errorf("ConsumerNames got %v", got)
	}

	// Add out of order: PEL must stay sorted.
	for _, ms := range []uint64{4, 2, 3} {
		g.AddPending(&PendingEntry{ID: StreamID{ms, 0}, Consumer: c1, DeliveryTime: 10, DeliveryCount: 1})
	}
	if got := g.PendingIDs(); !slices.Equal(got, []StreamID{{2, 0}, {3, 0}, {4, 0}}) {
		t.Errorf("PendingIDs got %v", got)
	}
	// Claim: move 3-0 to c2 by re-adding with the new owner.
	old := g.PEL[StreamID{3, 0}]
	delete(old.Consumer.PEL, old.ID)
	g.AddPending(&PendingEntry{ID: old.ID, Consumer: c2, DeliveryTime: 20, DeliveryCount: 2})
	if len(g.PEL) != 3 || len(c1.PEL) != 2 || len(c2.PEL) != 1 || g.PEL[old.ID].DeliveryCount != 2 {
		t.Errorf("claim wrong: group=%d c1=%d c2=%d", len(g.PEL), len(c1.PEL), len(c2.PEL))
	}
	var ranged []StreamID
	g.PendingRange(StreamID{3, 0}, StreamID{9, 0}, func(pe *PendingEntry) bool {
		ranged = append(ranged, pe.ID)
		return true
	})
	if !slices.Equal(ranged, []StreamID{{3, 0}, {4, 0}}) {
		t.Errorf("PendingRange got %v", ranged)
	}
	n := 0
	g.PendingRange(StreamID{}, MaxStreamID, func(*PendingEntry) bool { n++; return false })
	if n != 1 {
		t.Errorf("early stop iterations got %d", n)
	}
	// Ack.
	if !g.RemovePending(StreamID{2, 0}) || g.RemovePending(StreamID{2, 0}) {
		t.Error("RemovePending should report true then false")
	}
	if len(g.PEL) != 2 || len(c1.PEL) != 1 || len(g.PendingIDs()) != 2 {
		t.Errorf("after ack group=%d c1=%d order=%d", len(g.PEL), len(c1.PEL), len(g.PendingIDs()))
	}
	if s.Bytes() <= 0 {
		t.Error("Bytes with groups should be positive")
	}
}

func TestStream_Lag(t *testing.T) {
	s := streamWith(5)
	g, _ := s.CreateGroup("g", StreamID{3, 0}, 3)
	if lag, ok := s.Lag(g); !ok || lag != 2 {
		t.Errorf("Lag got %d,%v, want 2,true", lag, ok)
	}
	// A tombstone after the group position makes the counter unreliable.
	s.Delete(StreamID{4, 0})
	if lag, ok := s.Lag(g); ok {
		t.Errorf("Lag with tombstone got %d,%v, want unknown", lag, ok)
	}
	if !NewStream().RangeHasTombstones(nil, nil) == false {
		t.Error("empty stream has no tombstones")
	}
	empty := NewStream()
	eg, _ := empty.CreateGroup("g", StreamID{}, 0)
	if lag, ok := empty.Lag(eg); !ok || lag != 0 {
		t.Errorf("empty Lag got %d,%v", lag, ok)
	}
}

func TestStream_EstimateDistance(t *testing.T) {
	s := streamWith(5)
	tests := []struct {
		name string
		id   StreamID
		want int64
	}{
		{"before first", StreamID{0, 1}, 0},
		{"first", StreamID{1, 0}, 1},
		{"last", StreamID{5, 0}, 5},
		{"past last", StreamID{9, 0}, InvalidEntriesRead},
		{"middle with no tombstones", StreamID{3, 0}, InvalidEntriesRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.EstimateDistance(tc.id); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
	if got := NewStream().EstimateDistance(StreamID{1, 0}); got != 0 {
		t.Errorf("never-written stream got %d, want 0", got)
	}
}

func TestStream_RangeHasTombstones(t *testing.T) {
	s := streamWith(5)
	if s.RangeHasTombstones(nil, nil) {
		t.Error("no deletions yet")
	}
	s.Delete(StreamID{3, 0})
	lo, hi := StreamID{4, 0}, StreamID{9, 0}
	if s.RangeHasTombstones(&lo, &hi) {
		t.Error("range above tombstone reported tombstones")
	}
	if !s.RangeHasTombstones(nil, nil) {
		t.Error("full range should include tombstone")
	}
}

func TestStream_Clone_IsDeep(t *testing.T) {
	s := streamWith(3)
	g, _ := s.CreateGroup("g", StreamID{1, 0}, 1)
	c, _ := g.Consumer("c", 5, true)
	g.AddPending(&PendingEntry{ID: StreamID{2, 0}, Consumer: c, DeliveryTime: 7, DeliveryCount: 3})
	cl := s.Clone()

	s.Entries[0].Fields[1][0] = 'X'
	s.Append(StreamID{9, 0}, fields("z", "z"))
	g.RemovePending(StreamID{2, 0})

	if cl.Len() != 3 || string(cl.Entries[0].Fields[1]) != "1" {
		t.Errorf("clone entries mutated: len=%d f=%q", cl.Len(), cl.Entries[0].Fields[1])
	}
	cg := cl.Groups["g"]
	if cg == nil || len(cg.PEL) != 1 {
		t.Fatalf("clone group PEL wrong: %+v", cg)
	}
	pe := cg.PEL[StreamID{2, 0}]
	if pe.DeliveryCount != 3 || pe.Consumer == c || pe.Consumer.Name != "c" || len(pe.Consumer.PEL) != 1 {
		t.Errorf("clone PEL entry wrong: %+v", pe)
	}
	if cl.LastID != (StreamID{3, 0}) || cl.EntriesAdded != 3 {
		t.Errorf("clone counters got %v/%d", cl.LastID, cl.EntriesAdded)
	}
}
