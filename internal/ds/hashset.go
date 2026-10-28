package ds

// HashFieldOverhead and SetMemberOverhead are estimated per-entry overheads.
const (
	HashFieldOverhead = 48
	SetMemberOverhead = 40
)

// Hash is a field/value map with optional per-field expiration (Redis 7.4
// HEXPIRE family).
type Hash struct {
	m     map[string][]byte
	exp   map[string]int64 // field -> absolute unix ms
	bytes int64
}

// NewHash returns an empty hash.
func NewHash() *Hash { return &Hash{m: make(map[string][]byte)} }

// Bytes returns the estimated memory usage.
func (h *Hash) Bytes() int64 {
	return h.bytes + int64(len(h.m))*HashFieldOverhead + int64(len(h.exp))*24
}

func (h *Hash) expired(f string, now int64) bool {
	if h.exp == nil {
		return false
	}
	t, ok := h.exp[f]
	return ok && t <= now
}

// Get returns the value of field f unless missing or expired.
func (h *Hash) Get(f string, now int64) ([]byte, bool) {
	v, ok := h.m[f]
	if !ok || h.expired(f, now) {
		return nil, false
	}
	return v, true
}

// Set stores f=v, clearing any field TTL. Returns true when f is new.
func (h *Hash) Set(f string, v []byte, now int64) bool {
	old, ok := h.m[f]
	if ok && h.expired(f, now) {
		ok = false
	}
	if _, had := h.m[f]; had {
		h.bytes -= int64(len(f) + len(old))
	}
	h.m[f] = v
	h.bytes += int64(len(f) + len(v))
	if h.exp != nil {
		delete(h.exp, f)
	}
	return !ok
}

// SetKeepTTL stores f=v keeping any existing field TTL.
func (h *Hash) SetKeepTTL(f string, v []byte) {
	if old, had := h.m[f]; had {
		h.bytes -= int64(len(f) + len(old))
	}
	h.m[f] = v
	h.bytes += int64(len(f) + len(v))
}

// Del removes f. Returns true if it existed (and was not expired).
func (h *Hash) Del(f string, now int64) bool {
	v, ok := h.m[f]
	if !ok {
		return false
	}
	live := !h.expired(f, now)
	delete(h.m, f)
	h.bytes -= int64(len(f) + len(v))
	if h.exp != nil {
		delete(h.exp, f)
	}
	return live
}

// Len returns the number of live fields.
func (h *Hash) Len(now int64) int {
	n := len(h.m)
	for _, t := range h.exp {
		if t <= now {
			n--
		}
	}
	return n
}

// RawLen returns the number of stored fields including expired ones.
func (h *Hash) RawLen() int { return len(h.m) }

// Purge removes expired fields and returns how many were removed.
func (h *Hash) Purge(now int64) int {
	n := 0
	for f, t := range h.exp {
		if t <= now {
			v := h.m[f]
			delete(h.m, f)
			delete(h.exp, f)
			h.bytes -= int64(len(f) + len(v))
			n++
		}
	}
	return n
}

// ForEach iterates live fields; stop by returning false.
func (h *Hash) ForEach(now int64, fn func(f string, v []byte) bool) {
	for f, v := range h.m {
		if h.expired(f, now) {
			continue
		}
		if !fn(f, v) {
			return
		}
	}
}

// TTL returns the field's absolute expiry (ms), 0 if none, and whether the
// field exists.
func (h *Hash) TTL(f string, now int64) (int64, bool) {
	if _, ok := h.m[f]; !ok || h.expired(f, now) {
		return 0, false
	}
	if h.exp == nil {
		return 0, true
	}
	return h.exp[f], true
}

// SetTTL sets the field's absolute expiry in ms (0 removes it).
func (h *Hash) SetTTL(f string, at int64) {
	if at == 0 {
		if h.exp != nil {
			delete(h.exp, f)
			if len(h.exp) == 0 {
				h.exp = nil
			}
		}
		return
	}
	if h.exp == nil {
		h.exp = make(map[string]int64)
	}
	h.exp[f] = at
}

// HasTTLs reports whether any field has a TTL.
func (h *Hash) HasTTLs() bool { return len(h.exp) > 0 }

// MinTTL returns the earliest field expiry (0 when none).
func (h *Hash) MinTTL() int64 {
	var m int64
	for _, t := range h.exp {
		if m == 0 || t < m {
			m = t
		}
	}
	return m
}

// Clone returns a deep copy.
func (h *Hash) Clone() *Hash {
	n := &Hash{m: make(map[string][]byte, len(h.m)), bytes: h.bytes}
	for f, v := range h.m {
		n.m[f] = append([]byte(nil), v...)
	}
	if h.exp != nil {
		n.exp = make(map[string]int64, len(h.exp))
		for f, t := range h.exp {
			n.exp[f] = t
		}
	}
	return n
}

// Set is an unordered collection of unique strings.
type Set struct {
	m     map[string]struct{}
	bytes int64
}

// NewSet returns an empty set.
func NewSet() *Set { return &Set{m: make(map[string]struct{})} }

// Bytes returns the estimated memory usage.
func (s *Set) Bytes() int64 { return s.bytes + int64(len(s.m))*SetMemberOverhead }

// Add inserts m; returns true if it was new.
func (s *Set) Add(m string) bool {
	if _, ok := s.m[m]; ok {
		return false
	}
	s.m[m] = struct{}{}
	s.bytes += int64(len(m))
	return true
}

// Rem removes m; returns true if it existed.
func (s *Set) Rem(m string) bool {
	if _, ok := s.m[m]; !ok {
		return false
	}
	delete(s.m, m)
	s.bytes -= int64(len(m))
	return true
}

// Has reports membership.
func (s *Set) Has(m string) bool { _, ok := s.m[m]; return ok }

// Len returns the cardinality.
func (s *Set) Len() int { return len(s.m) }

// ForEach iterates members; stop by returning false.
func (s *Set) ForEach(fn func(m string) bool) {
	for m := range s.m {
		if !fn(m) {
			return
		}
	}
}

// Members returns all members.
func (s *Set) Members() []string {
	out := make([]string, 0, len(s.m))
	for m := range s.m {
		out = append(out, m)
	}
	return out
}

// Clone returns a copy.
func (s *Set) Clone() *Set {
	n := &Set{m: make(map[string]struct{}, len(s.m)), bytes: s.bytes}
	for m := range s.m {
		n.m[m] = struct{}{}
	}
	return n
}
