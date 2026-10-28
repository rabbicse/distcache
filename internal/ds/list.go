package ds

// List is a double-ended queue backed by a growable ring buffer. Push/pop at
// both ends are O(1) amortised and index access is O(1), which beats Redis'
// quicklist for LINDEX/LSET on long lists.
type List struct {
	buf   [][]byte
	head  int
	n     int
	bytes int64
}

// ListElemOverhead is the estimated per-element memory overhead in bytes.
const ListElemOverhead = 24

// NewList returns an empty list.
func NewList() *List { return &List{} }

// Len returns the number of elements.
func (l *List) Len() int { return l.n }

// Bytes returns the estimated memory used by the elements.
func (l *List) Bytes() int64 { return l.bytes + int64(cap(l.buf))*ListElemOverhead }

func (l *List) grow() {
	nc := len(l.buf) * 2
	if nc < 8 {
		nc = 8
	}
	nb := make([][]byte, nc)
	for i := 0; i < l.n; i++ {
		nb[i] = l.buf[(l.head+i)%len(l.buf)]
	}
	l.buf = nb
	l.head = 0
}

func (l *List) shrink() {
	if len(l.buf) > 64 && l.n < len(l.buf)/4 {
		nb := make([][]byte, len(l.buf)/2)
		for i := 0; i < l.n; i++ {
			nb[i] = l.buf[(l.head+i)%len(l.buf)]
		}
		l.buf = nb
		l.head = 0
	}
}

func (l *List) pos(i int) int { return (l.head + i) % len(l.buf) }

// PushBack appends v.
func (l *List) PushBack(v []byte) {
	if l.n == len(l.buf) {
		l.grow()
	}
	l.buf[l.pos(l.n)] = v
	l.n++
	l.bytes += int64(len(v))
}

// PushFront prepends v.
func (l *List) PushFront(v []byte) {
	if l.n == len(l.buf) {
		l.grow()
	}
	l.head = (l.head - 1 + len(l.buf)) % len(l.buf)
	l.buf[l.head] = v
	l.n++
	l.bytes += int64(len(v))
}

// PopFront removes and returns the first element.
func (l *List) PopFront() ([]byte, bool) {
	if l.n == 0 {
		return nil, false
	}
	v := l.buf[l.head]
	l.buf[l.head] = nil
	l.head = (l.head + 1) % len(l.buf)
	l.n--
	l.bytes -= int64(len(v))
	l.shrink()
	return v, true
}

// PopBack removes and returns the last element.
func (l *List) PopBack() ([]byte, bool) {
	if l.n == 0 {
		return nil, false
	}
	p := l.pos(l.n - 1)
	v := l.buf[p]
	l.buf[p] = nil
	l.n--
	l.bytes -= int64(len(v))
	l.shrink()
	return v, true
}

// Index returns element i (0 based, must be in range).
func (l *List) Index(i int) []byte { return l.buf[l.pos(i)] }

// Set replaces element i.
func (l *List) Set(i int, v []byte) {
	p := l.pos(i)
	l.bytes += int64(len(v)) - int64(len(l.buf[p]))
	l.buf[p] = v
}

// Slice returns a copy of elements [start, stop] inclusive (in range).
func (l *List) Slice(start, stop int) [][]byte {
	if start > stop {
		return nil
	}
	out := make([][]byte, 0, stop-start+1)
	for i := start; i <= stop; i++ {
		out = append(out, l.buf[l.pos(i)])
	}
	return out
}

// All returns every element in order.
func (l *List) All() [][]byte { return l.Slice(0, l.n-1) }

// Insert inserts v at index i (0 <= i <= Len) shifting the tail.
func (l *List) Insert(i int, v []byte) {
	if i == 0 {
		l.PushFront(v)
		return
	}
	if i == l.n {
		l.PushBack(v)
		return
	}
	l.PushBack(nil)
	for j := l.n - 1; j > i; j-- {
		l.buf[l.pos(j)] = l.buf[l.pos(j-1)]
	}
	l.buf[l.pos(i)] = v
	l.bytes += int64(len(v))
}

// RemoveAt deletes element i.
func (l *List) RemoveAt(i int) {
	v := l.buf[l.pos(i)]
	if i < l.n/2 {
		for j := i; j > 0; j-- {
			l.buf[l.pos(j)] = l.buf[l.pos(j-1)]
		}
		l.buf[l.head] = nil
		l.head = (l.head + 1) % len(l.buf)
	} else {
		for j := i; j < l.n-1; j++ {
			l.buf[l.pos(j)] = l.buf[l.pos(j+1)]
		}
		l.buf[l.pos(l.n-1)] = nil
	}
	l.n--
	l.bytes -= int64(len(v))
	l.shrink()
}

// Trim keeps only elements in [start, stop] (already normalised; start>stop
// empties the list).
func (l *List) Trim(start, stop int) {
	if start > stop || start >= l.n {
		*l = List{}
		return
	}
	kept := l.Slice(start, stop)
	nl := List{}
	for _, v := range kept {
		nl.PushBack(v)
	}
	*l = nl
}

// Filter rebuilds the list keeping elements for which keep returns true.
func (l *List) Filter(keep func(i int, v []byte) bool) int {
	removed := 0
	nl := List{}
	for i := 0; i < l.n; i++ {
		v := l.buf[l.pos(i)]
		if keep(i, v) {
			nl.PushBack(v)
		} else {
			removed++
		}
	}
	*l = nl
	return removed
}

// Clone returns a deep copy.
func (l *List) Clone() *List {
	nl := &List{}
	for i := 0; i < l.n; i++ {
		nl.PushBack(append([]byte(nil), l.buf[l.pos(i)]...))
	}
	return nl
}
