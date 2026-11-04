package ds

import (
	"slices"
	"testing"
)

func TestHash_SetGetDel(t *testing.T) {
	h := NewHash()
	if !h.Set("f", []byte("v1"), 0) {
		t.Error("first Set should report new field")
	}
	if h.Set("f", []byte("v22"), 0) {
		t.Error("second Set should not report new field")
	}
	v, ok := h.Get("f", 0)
	if !ok || string(v) != "v22" {
		t.Errorf("Get got %q,%v, want v22,true", v, ok)
	}
	if _, ok := h.Get("missing", 0); ok {
		t.Error("Get of missing field reported ok")
	}
	if h.Len(0) != 1 || h.RawLen() != 1 {
		t.Errorf("Len got %d/%d, want 1/1", h.Len(0), h.RawLen())
	}
	if !h.Del("f", 0) {
		t.Error("Del of live field should report true")
	}
	if h.Del("f", 0) {
		t.Error("Del of missing field should report false")
	}
	if h.Bytes() != 0 {
		t.Errorf("Bytes after delete got %d, want 0", h.Bytes())
	}
}

func TestHash_FieldTTL(t *testing.T) {
	h := NewHash()
	h.Set("a", []byte("1"), 0)
	h.Set("b", []byte("2"), 0)
	h.Set("c", []byte("3"), 0)
	if h.HasTTLs() || h.MinTTL() != 0 {
		t.Fatal("fresh hash should have no TTLs")
	}
	h.SetTTL("a", 1000)
	h.SetTTL("b", 500)

	if at, ok := h.TTL("a", 0); !ok || at != 1000 {
		t.Errorf("TTL(a) got %d,%v, want 1000,true", at, ok)
	}
	if at, ok := h.TTL("c", 0); !ok || at != 0 {
		t.Errorf("TTL(c) got %d,%v, want 0,true", at, ok)
	}
	if got := h.MinTTL(); got != 500 {
		t.Errorf("MinTTL got %d, want 500", got)
	}
	// At now=600, b is expired but still stored.
	if _, ok := h.Get("b", 600); ok {
		t.Error("expired field visible via Get")
	}
	if _, ok := h.TTL("b", 600); ok {
		t.Error("expired field visible via TTL")
	}
	if h.Len(600) != 2 || h.RawLen() != 3 {
		t.Errorf("Len/RawLen got %d/%d, want 2/3", h.Len(600), h.RawLen())
	}
	var seen []string
	h.ForEach(600, func(f string, _ []byte) bool { seen = append(seen, f); return true })
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"a", "c"}) {
		t.Errorf("ForEach got %v, want [a c]", seen)
	}
	// Set on an expired field reports it as new, and clears the TTL.
	if !h.Set("b", []byte("new"), 600) {
		t.Error("Set over expired field should report new")
	}
	if _, ok := h.TTL("b", 600); !ok {
		t.Error("field b should be live again")
	}
	// Purge removes expired fields.
	if n := h.Purge(1500); n != 1 {
		t.Errorf("Purge got %d, want 1 (field a)", n)
	}
	if h.RawLen() != 2 {
		t.Errorf("RawLen after purge got %d, want 2", h.RawLen())
	}
	h.SetTTL("c", 0)
	h.SetTTL("a", 0) // clearing a never-set TTL must be harmless
	if h.HasTTLs() {
		t.Error("HasTTLs true after clearing")
	}
}

func TestHash_Del_ExpiredReportsFalse(t *testing.T) {
	h := NewHash()
	h.Set("a", []byte("1"), 0)
	h.SetTTL("a", 10)
	if h.Del("a", 20) {
		t.Error("Del of expired field should report false")
	}
	if h.RawLen() != 0 {
		t.Error("expired field should still be physically removed")
	}
}

func TestHash_SetKeepTTL(t *testing.T) {
	h := NewHash()
	h.Set("a", []byte("1"), 0)
	h.SetTTL("a", 100)
	h.SetKeepTTL("a", []byte("two"))
	if at, _ := h.TTL("a", 0); at != 100 {
		t.Errorf("TTL got %d, want 100", at)
	}
	h.SetKeepTTL("new", []byte("x"))
	if v, ok := h.Get("new", 0); !ok || string(v) != "x" {
		t.Errorf("Get got %q,%v", v, ok)
	}
	h.Set("a", []byte("three"), 0)
	if at, _ := h.TTL("a", 0); at != 0 {
		t.Errorf("Set should clear TTL, got %d", at)
	}
}

func TestHash_ForEach_EarlyStop(t *testing.T) {
	h := NewHash()
	for _, f := range []string{"a", "b", "c"} {
		h.Set(f, []byte(f), 0)
	}
	n := 0
	h.ForEach(0, func(string, []byte) bool { n++; return false })
	if n != 1 {
		t.Errorf("iterations got %d, want 1", n)
	}
}

func TestHash_Clone_IsDeep(t *testing.T) {
	h := NewHash()
	h.Set("a", []byte("val"), 0)
	h.SetTTL("a", 99)
	c := h.Clone()
	v, _ := h.Get("a", 0)
	v[0] = 'X'
	h.SetTTL("a", 5)
	h.Set("b", []byte("z"), 0)
	if got, _ := c.Get("a", 0); string(got) != "val" {
		t.Errorf("clone value mutated: %q", got)
	}
	if at, _ := c.TTL("a", 0); at != 99 {
		t.Errorf("clone TTL got %d, want 99", at)
	}
	if c.RawLen() != 1 || c.Bytes() == 0 {
		t.Errorf("clone shape wrong: len %d bytes %d", c.RawLen(), c.Bytes())
	}
}

func TestSet_AddRemHas(t *testing.T) {
	s := NewSet()
	if !s.Add("a") || s.Add("a") {
		t.Error("Add should report new then duplicate")
	}
	s.Add("bb")
	if !s.Has("a") || s.Has("zz") {
		t.Error("Has wrong")
	}
	if s.Len() != 2 {
		t.Errorf("Len got %d, want 2", s.Len())
	}
	if !s.Rem("a") || s.Rem("a") {
		t.Error("Rem should report true then false")
	}
	m := s.Members()
	if !slices.Equal(m, []string{"bb"}) {
		t.Errorf("Members got %v, want [bb]", m)
	}
	s.Rem("bb")
	if s.Bytes() != 0 {
		t.Errorf("Bytes got %d, want 0", s.Bytes())
	}
}

func TestSet_ForEach(t *testing.T) {
	s := NewSet()
	for _, m := range []string{"1", "2", "3"} {
		s.Add(m)
	}
	var seen []string
	s.ForEach(func(m string) bool { seen = append(seen, m); return true })
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"1", "2", "3"}) {
		t.Errorf("got %v", seen)
	}
	n := 0
	s.ForEach(func(string) bool { n++; return false })
	if n != 1 {
		t.Errorf("early stop iterations got %d, want 1", n)
	}
}

func TestSet_Clone_IsIndependent(t *testing.T) {
	s := NewSet()
	s.Add("a")
	c := s.Clone()
	s.Add("b")
	s.Rem("a")
	if !c.Has("a") || c.Has("b") || c.Len() != 1 {
		t.Errorf("clone not independent: %v", c.Members())
	}
	if c.Bytes() != SetMemberOverhead+1 {
		t.Errorf("clone Bytes got %d", c.Bytes())
	}
}
