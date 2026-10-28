package ds

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// StreamID identifies a stream entry (<ms>-<seq>).
type StreamID struct{ Ms, Seq uint64 }

// MaxStreamID is the largest possible ID.
var MaxStreamID = StreamID{math.MaxUint64, math.MaxUint64}

// String formats the ID.
func (a StreamID) String() string { return fmt.Sprintf("%d-%d", a.Ms, a.Seq) }

// Cmp compares two IDs.
func (a StreamID) Cmp(b StreamID) int {
	switch {
	case a.Ms < b.Ms:
		return -1
	case a.Ms > b.Ms:
		return 1
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	return 0
}

// IsZero reports 0-0.
func (a StreamID) IsZero() bool { return a.Ms == 0 && a.Seq == 0 }

// Incr returns the next ID; ok=false on overflow.
func (a StreamID) Incr() (StreamID, bool) {
	if a.Seq == math.MaxUint64 {
		if a.Ms == math.MaxUint64 {
			return a, false
		}
		return StreamID{a.Ms + 1, 0}, true
	}
	return StreamID{a.Ms, a.Seq + 1}, true
}

// Decr returns the previous ID; ok=false on underflow.
func (a StreamID) Decr() (StreamID, bool) {
	if a.Seq == 0 {
		if a.Ms == 0 {
			return a, false
		}
		return StreamID{a.Ms - 1, math.MaxUint64}, true
	}
	return StreamID{a.Ms, a.Seq - 1}, true
}

// ParseStreamID parses "ms-seq" or "ms". When seq is missing it is set to
// missingSeq. seqGiven reports whether the sequence part was present, and
// seqAuto whether it was "*".
func ParseStreamID(s string, missingSeq uint64) (id StreamID, seqGiven, seqAuto bool, ok bool) {
	if s == "" || len(s) > 127 {
		return id, false, false, false
	}
	msPart, seqPart, has := strings.Cut(s, "-")
	ms, err := strconv.ParseUint(msPart, 10, 64)
	if err != nil {
		return id, false, false, false
	}
	id.Ms = ms
	if !has {
		id.Seq = missingSeq
		return id, false, false, true
	}
	if seqPart == "*" {
		return id, true, true, true
	}
	seq, err := strconv.ParseUint(seqPart, 10, 64)
	if err != nil {
		return id, false, false, false
	}
	id.Seq = seq
	return id, true, false, true
}

// StreamEntry is one entry with its flattened field/value pairs.
type StreamEntry struct {
	ID     StreamID
	Fields [][]byte
}

// PendingEntry is a PEL item.
type PendingEntry struct {
	ID            StreamID
	Consumer      *Consumer
	DeliveryTime  int64
	DeliveryCount int64
}

// Consumer is a member of a consumer group.
type Consumer struct {
	Name       string
	SeenTime   int64
	ActiveTime int64
	PEL        map[StreamID]*PendingEntry
}

// ConsumerGroup is a stream consumer group.
type ConsumerGroup struct {
	Name        string
	LastID      StreamID
	EntriesRead int64 // -1 when unknown
	PEL         map[StreamID]*PendingEntry
	pelOrder    []StreamID
	Consumers   map[string]*Consumer
}

// InvalidEntriesRead marks an unknown entries-read counter.
const InvalidEntriesRead = -1

// Stream is an append-only log.
type Stream struct {
	Entries      []StreamEntry
	LastID       StreamID
	MaxDeletedID StreamID
	EntriesAdded uint64
	Groups       map[string]*ConsumerGroup
	bytes        int64
}

// NewStream creates an empty stream.
func NewStream() *Stream { return &Stream{} }

// Len returns the number of entries.
func (s *Stream) Len() int { return len(s.Entries) }

// Bytes returns estimated memory usage.
func (s *Stream) Bytes() int64 {
	b := s.bytes + int64(len(s.Entries))*40
	for _, g := range s.Groups {
		b += int64(len(g.PEL))*64 + int64(len(g.Consumers))*64
	}
	return b
}

// FirstID returns the first entry ID (0-0 if empty).
func (s *Stream) FirstID() StreamID {
	if len(s.Entries) == 0 {
		return StreamID{}
	}
	return s.Entries[0].ID
}

func entryBytes(e *StreamEntry) int64 {
	n := int64(16)
	for _, f := range e.Fields {
		n += int64(len(f)) + 8
	}
	return n
}

// Append adds an entry with an ID strictly greater than LastID.
func (s *Stream) Append(id StreamID, fields [][]byte) {
	e := StreamEntry{ID: id, Fields: fields}
	s.Entries = append(s.Entries, e)
	s.bytes += entryBytes(&e)
	s.LastID = id
	s.EntriesAdded++
}

// Seek returns the index of the first entry with ID >= id.
func (s *Stream) Seek(id StreamID) int {
	return sort.Search(len(s.Entries), func(i int) bool { return s.Entries[i].ID.Cmp(id) >= 0 })
}

// Get returns the entry with the given ID.
func (s *Stream) Get(id StreamID) (*StreamEntry, bool) {
	i := s.Seek(id)
	if i < len(s.Entries) && s.Entries[i].ID == id {
		return &s.Entries[i], true
	}
	return nil, false
}

// Delete removes the entry with id; returns true if found.
func (s *Stream) Delete(id StreamID) bool {
	i := s.Seek(id)
	if i >= len(s.Entries) || s.Entries[i].ID != id {
		return false
	}
	s.bytes -= entryBytes(&s.Entries[i])
	copy(s.Entries[i:], s.Entries[i+1:])
	s.Entries[len(s.Entries)-1] = StreamEntry{}
	s.Entries = s.Entries[:len(s.Entries)-1]
	if id.Cmp(s.MaxDeletedID) > 0 {
		s.MaxDeletedID = id
	}
	return true
}

// TrimHead removes the first n entries.
func (s *Stream) TrimHead(n int) {
	if n <= 0 {
		return
	}
	if n > len(s.Entries) {
		n = len(s.Entries)
	}
	for i := 0; i < n; i++ {
		s.bytes -= entryBytes(&s.Entries[i])
	}
	last := s.Entries[n-1].ID
	rest := make([]StreamEntry, len(s.Entries)-n)
	copy(rest, s.Entries[n:])
	s.Entries = rest
	if last.Cmp(s.MaxDeletedID) > 0 {
		s.MaxDeletedID = last
	}
}

// RangeHasTombstones mirrors streamRangeHasTombstones().
func (s *Stream) RangeHasTombstones(start, end *StreamID) bool {
	if len(s.Entries) == 0 || s.MaxDeletedID.IsZero() {
		return false
	}
	st := StreamID{}
	if start != nil {
		st = *start
	}
	en := MaxStreamID
	if end != nil {
		en = *end
	}
	return st.Cmp(s.MaxDeletedID) <= 0 && s.MaxDeletedID.Cmp(en) <= 0
}

// EstimateDistance mirrors streamEstimateDistanceFromFirstEverEntry().
func (s *Stream) EstimateDistance(id StreamID) int64 {
	if s.EntriesAdded == 0 {
		return 0
	}
	if len(s.Entries) == 0 && id.Cmp(s.LastID) < 1 {
		return int64(s.EntriesAdded)
	}
	cmpLast := id.Cmp(s.LastID)
	if cmpLast == 0 {
		return int64(s.EntriesAdded)
	} else if cmpLast > 0 {
		return InvalidEntriesRead
	}
	first := s.FirstID()
	cmpFirst := id.Cmp(first)
	if s.MaxDeletedID.IsZero() || s.MaxDeletedID.Cmp(first) < 0 {
		if cmpFirst < 0 {
			return int64(s.EntriesAdded) - int64(len(s.Entries))
		} else if cmpFirst == 0 {
			return int64(s.EntriesAdded) - int64(len(s.Entries)) + 1
		}
	}
	return InvalidEntriesRead
}

// Lag computes the consumer group lag; ok=false when unknown (null).
func (s *Stream) Lag(g *ConsumerGroup) (int64, bool) {
	if s.EntriesAdded == 0 {
		return 0, true
	}
	if g.EntriesRead != InvalidEntriesRead && !s.RangeHasTombstones(&g.LastID, nil) {
		return int64(s.EntriesAdded) - g.EntriesRead, true
	}
	er := s.EstimateDistance(g.LastID)
	if er != InvalidEntriesRead {
		return int64(s.EntriesAdded) - er, true
	}
	return 0, false
}

// CreateGroup adds a group; returns false if it exists.
func (s *Stream) CreateGroup(name string, last StreamID, entriesRead int64) (*ConsumerGroup, bool) {
	if s.Groups == nil {
		s.Groups = make(map[string]*ConsumerGroup)
	}
	if _, ok := s.Groups[name]; ok {
		return nil, false
	}
	g := &ConsumerGroup{Name: name, LastID: last, EntriesRead: entriesRead,
		PEL: make(map[StreamID]*PendingEntry), Consumers: make(map[string]*Consumer)}
	s.Groups[name] = g
	return g, true
}

// GroupNames returns group names sorted (Redis rax order).
func (s *Stream) GroupNames() []string {
	names := make([]string, 0, len(s.Groups))
	for n := range s.Groups {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ConsumerNames returns consumer names sorted.
func (g *ConsumerGroup) ConsumerNames() []string {
	names := make([]string, 0, len(g.Consumers))
	for n := range g.Consumers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Consumer looks up or creates a consumer. created reports creation.
func (g *ConsumerGroup) Consumer(name string, now int64, create bool) (*Consumer, bool) {
	if c, ok := g.Consumers[name]; ok {
		return c, false
	}
	if !create {
		return nil, false
	}
	c := &Consumer{Name: name, SeenTime: now, ActiveTime: -1, PEL: make(map[StreamID]*PendingEntry)}
	g.Consumers[name] = c
	return c, true
}

// AddPending inserts into the group PEL (keeps order).
func (g *ConsumerGroup) AddPending(pe *PendingEntry) {
	if _, ok := g.PEL[pe.ID]; !ok {
		i := sort.Search(len(g.pelOrder), func(i int) bool { return g.pelOrder[i].Cmp(pe.ID) >= 0 })
		g.pelOrder = append(g.pelOrder, StreamID{})
		copy(g.pelOrder[i+1:], g.pelOrder[i:])
		g.pelOrder[i] = pe.ID
	}
	g.PEL[pe.ID] = pe
	pe.Consumer.PEL[pe.ID] = pe
}

// RemovePending deletes id from the group and owning consumer PEL.
func (g *ConsumerGroup) RemovePending(id StreamID) bool {
	pe, ok := g.PEL[id]
	if !ok {
		return false
	}
	delete(g.PEL, id)
	delete(pe.Consumer.PEL, id)
	i := sort.Search(len(g.pelOrder), func(i int) bool { return g.pelOrder[i].Cmp(id) >= 0 })
	if i < len(g.pelOrder) && g.pelOrder[i] == id {
		g.pelOrder = append(g.pelOrder[:i], g.pelOrder[i+1:]...)
	}
	return true
}

// PendingRange iterates PEL entries with start <= id <= end in order.
func (g *ConsumerGroup) PendingRange(start, end StreamID, fn func(pe *PendingEntry) bool) {
	i := sort.Search(len(g.pelOrder), func(i int) bool { return g.pelOrder[i].Cmp(start) >= 0 })
	for ; i < len(g.pelOrder); i++ {
		id := g.pelOrder[i]
		if id.Cmp(end) > 0 {
			return
		}
		if !fn(g.PEL[id]) {
			return
		}
	}
}

// PendingIDs returns the ordered PEL IDs.
func (g *ConsumerGroup) PendingIDs() []StreamID { return g.pelOrder }

// Clone deep-copies the stream including groups.
func (s *Stream) Clone() *Stream {
	n := &Stream{LastID: s.LastID, MaxDeletedID: s.MaxDeletedID, EntriesAdded: s.EntriesAdded, bytes: s.bytes}
	n.Entries = make([]StreamEntry, len(s.Entries))
	for i, e := range s.Entries {
		f := make([][]byte, len(e.Fields))
		for j, b := range e.Fields {
			f[j] = append([]byte(nil), b...)
		}
		n.Entries[i] = StreamEntry{ID: e.ID, Fields: f}
	}
	for name, g := range s.Groups {
		ng, _ := n.CreateGroup(name, g.LastID, g.EntriesRead)
		for cname, c := range g.Consumers {
			nc, _ := ng.Consumer(cname, c.SeenTime, true)
			nc.ActiveTime = c.ActiveTime
		}
		for _, id := range g.pelOrder {
			pe := g.PEL[id]
			ng.AddPending(&PendingEntry{ID: id, Consumer: ng.Consumers[pe.Consumer.Name],
				DeliveryTime: pe.DeliveryTime, DeliveryCount: pe.DeliveryCount})
		}
	}
	return n
}
