package storage

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"math"

	"github.com/rabbicse/distcache/internal/ds"
)

// Binary snapshot format ("DCSNAP"). It is used for point-in-time snapshots
// (SAVE/BGSAVE), the AOF preamble, replication full syncs and DUMP/RESTORE.
//
//	file   := "DCSNAP" version:u8 { record } 0xFF crc64:u64le
//	record := 0xFE db:uvarint            -- select database
//	        | [0xFD expire_ms:varint] type:u8 key:bytes value
//	bytes  := len:uvarint data

const (
	snapMagic   = "DCSNAP"
	snapVersion = 1
	opSelectDB  = 0xFE
	opAux       = 0xFC
	opExpire    = 0xFD
	opEOF       = 0xFF

	tString = 0
	tList   = 1
	tSet    = 2
	tZSet   = 3
	tHash   = 4
	tStream = 5
)

var crcTable = crc64.MakeTable(crc64.ECMA)

// Encoder writes a snapshot stream.
type Encoder struct {
	w   *bufio.Writer
	crc hash.Hash64
	n   int64
	db  int
	tmp [binary.MaxVarintLen64]byte
	err error
}

// NewEncoder writes the header to w.
func NewEncoder(w io.Writer) *Encoder {
	e := &Encoder{crc: crc64.New(crcTable), db: -1}
	e.w = bufio.NewWriterSize(w, 64*1024)
	e.raw([]byte(snapMagic))
	e.raw([]byte{snapVersion})
	return e
}

func (e *Encoder) raw(b []byte) {
	if e.err == nil {
		_, e.err = e.w.Write(b)
		e.crc.Write(b)
		e.n += int64(len(b))
	}
}

// Written returns the number of bytes encoded so far.
func (e *Encoder) Written() int64 { return e.n }

// Err returns the first error encountered.
func (e *Encoder) Err() error   { return e.err }
func (e *Encoder) byte1(b byte) { e.raw([]byte{b}) }
func (e *Encoder) uvarint(v uint64) {
	n := binary.PutUvarint(e.tmp[:], v)
	e.raw(e.tmp[:n])
}
func (e *Encoder) varint(v int64) {
	n := binary.PutVarint(e.tmp[:], v)
	e.raw(e.tmp[:n])
}
func (e *Encoder) bytes(b []byte) { e.uvarint(uint64(len(b))); e.raw(b) }
func (e *Encoder) str(s string)   { e.uvarint(uint64(len(s))); e.raw([]byte(s)) }
func (e *Encoder) f64(f float64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
	e.raw(b[:])
}

// WriteEntry appends one key.
func (e *Encoder) WriteEntry(db int, key string, ent *Entry) {
	if db != e.db {
		e.byte1(opSelectDB)
		e.uvarint(uint64(db))
		e.db = db
	}
	if ent.ExpireAt != 0 {
		e.byte1(opExpire)
		e.varint(ent.ExpireAt)
	}
	writeValue(e, key, ent.Value, true)
}

func writeValue(e *Encoder, key string, v any, withKey bool) {
	t := typeCode(v)
	e.byte1(t)
	if withKey {
		e.str(key)
	}
	switch x := v.(type) {
	case []byte:
		e.bytes(x)
	case *ds.List:
		e.uvarint(uint64(x.Len()))
		for i := 0; i < x.Len(); i++ {
			e.bytes(x.Index(i))
		}
	case *ds.Set:
		e.uvarint(uint64(x.Len()))
		x.ForEach(func(m string) bool { e.str(m); return true })
	case *ds.ZSet:
		e.uvarint(uint64(x.Len()))
		x.ForEach(func(m string, s float64) bool { e.str(m); e.f64(s); return true })
	case *ds.Hash:
		e.uvarint(uint64(x.RawLen()))
		x.ForEach(math.MinInt64, func(f string, val []byte) bool {
			e.str(f)
			e.bytes(val)
			at, _ := x.TTL(f, math.MinInt64)
			e.varint(at)
			return true
		})
	case *ds.Stream:
		writeStream(e, x)
	}
}

func writeID(e *Encoder, id ds.StreamID) { e.uvarint(id.Ms); e.uvarint(id.Seq) }

func writeStream(e *Encoder, s *ds.Stream) {
	e.uvarint(uint64(len(s.Entries)))
	for _, en := range s.Entries {
		writeID(e, en.ID)
		e.uvarint(uint64(len(en.Fields)))
		for _, f := range en.Fields {
			e.bytes(f)
		}
	}
	writeID(e, s.LastID)
	writeID(e, s.MaxDeletedID)
	e.uvarint(s.EntriesAdded)
	names := s.GroupNames()
	e.uvarint(uint64(len(names)))
	for _, n := range names {
		g := s.Groups[n]
		e.str(n)
		writeID(e, g.LastID)
		e.varint(g.EntriesRead)
		cn := g.ConsumerNames()
		e.uvarint(uint64(len(cn)))
		for _, c := range cn {
			con := g.Consumers[c]
			e.str(c)
			e.varint(con.SeenTime)
			e.varint(con.ActiveTime)
		}
		ids := g.PendingIDs()
		e.uvarint(uint64(len(ids)))
		for _, id := range ids {
			pe := g.PEL[id]
			writeID(e, id)
			e.str(pe.Consumer.Name)
			e.varint(pe.DeliveryTime)
			e.varint(pe.DeliveryCount)
		}
	}
}

func typeCode(v any) byte {
	switch v.(type) {
	case *ds.List:
		return tList
	case *ds.Set:
		return tSet
	case *ds.ZSet:
		return tZSet
	case *ds.Hash:
		return tHash
	case *ds.Stream:
		return tStream
	}
	return tString
}

// WriteAux writes an auxiliary record (e.g. function libraries).
func (e *Encoder) WriteAux(name string, data []byte) {
	e.byte1(opAux)
	e.str(name)
	e.bytes(data)
}

// Close writes the trailer and flushes.
func (e *Encoder) Close() error {
	e.byte1(opEOF)
	if e.err != nil {
		return e.err
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], e.crc.Sum64())
	if _, err := e.w.Write(b[:]); err != nil {
		return err
	}
	return e.w.Flush()
}

// DumpVersion is embedded in DUMP payloads.
const DumpVersion = 1

// EncodeValue serialises a single value for DUMP: type+value, a 2 byte
// version and a CRC64 of everything before it.
func EncodeValue(v any) []byte {
	var buf bytesBuffer
	e := &Encoder{crc: crc64.New(crcTable), db: -1}
	e.w = bufio.NewWriter(&buf)
	writeValue(e, "", v, false)
	_ = e.w.Flush()
	out := buf.b
	out = append(out, DumpVersion, 0)
	sum := crc64.Checksum(out, crcTable)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], sum)
	return append(out, b[:]...)
}

// DecodeValue parses a DUMP payload.
func DecodeValue(p []byte) (any, error) {
	if len(p) < 11 {
		return nil, ErrBadSnapshot
	}
	body := p[:len(p)-8]
	if crc64.Checksum(body, crcTable) != binary.LittleEndian.Uint64(p[len(p)-8:]) {
		return nil, ErrBadSnapshot
	}
	if body[len(body)-2] != DumpVersion {
		return nil, ErrBadSnapshot
	}
	data := body[:len(body)-2]
	br := &byteReader{r: bufio.NewReader(&bytesReader{b: data}), crc: crc64.New(crcTable)}
	t := br.byte1()
	v := readValue(br, t)
	if br.err != nil || v == nil {
		return nil, ErrBadSnapshot
	}
	return v, nil
}

type bytesBuffer struct{ b []byte }

func (w *bytesBuffer) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// ---- decoding ----

// Decoder reads a snapshot stream.
type Decoder struct {
	r   *bufio.Reader
	crc hash.Hash64
}

type byteReader struct {
	r   *bufio.Reader
	crc hash.Hash64
	err error
}

func (b *byteReader) ReadByte() (byte, error) {
	c, err := b.r.ReadByte()
	if err == nil {
		b.crc.Write([]byte{c})
	}
	return c, err
}

func (b *byteReader) read(n uint64) []byte {
	if b.err != nil {
		return nil
	}
	if n > 1<<32 {
		b.err = errors.New("snapshot: length too large")
		return nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(b.r, buf); err != nil {
		b.err = err
		return nil
	}
	b.crc.Write(buf)
	return buf
}

func (b *byteReader) byte1() byte {
	if b.err != nil {
		return 0
	}
	c, err := b.ReadByte()
	if err != nil {
		b.err = err
	}
	return c
}

func (b *byteReader) uvarint() uint64 {
	if b.err != nil {
		return 0
	}
	v, err := binary.ReadUvarint(b)
	if err != nil {
		b.err = err
	}
	return v
}

func (b *byteReader) varint() int64 {
	if b.err != nil {
		return 0
	}
	v, err := binary.ReadVarint(b)
	if err != nil {
		b.err = err
	}
	return v
}

func (b *byteReader) bytes() []byte { return b.read(b.uvarint()) }
func (b *byteReader) str() string   { return string(b.bytes()) }
func (b *byteReader) f64() float64 {
	buf := b.read(8)
	if buf == nil {
		return 0
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(buf))
}
func (b *byteReader) id() ds.StreamID { return ds.StreamID{Ms: b.uvarint(), Seq: b.uvarint()} }

// ErrBadSnapshot reports a corrupt or foreign file.
var ErrBadSnapshot = errors.New("snapshot: bad format or checksum")

// LoadFunc receives each decoded key.
type LoadFunc func(db int, key string, value any, expireAt int64) error

// Decode reads a full snapshot (header included) from r, calling fn for every
// key. It verifies the trailing checksum.
func Decode(r *bufio.Reader, fn LoadFunc) error { return DecodeWithAux(r, fn, nil) }

// AuxFunc receives auxiliary records.
type AuxFunc func(name string, data []byte) error

// DecodeWithAux is Decode with an auxiliary record callback.
func DecodeWithAux(r *bufio.Reader, fn LoadFunc, aux AuxFunc) error {
	br := &byteReader{r: r, crc: crc64.New(crcTable)}
	hdr := br.read(uint64(len(snapMagic) + 1))
	if br.err != nil {
		return br.err
	}
	if string(hdr[:len(snapMagic)]) != snapMagic {
		return ErrBadSnapshot
	}
	if hdr[len(snapMagic)] != snapVersion {
		return fmt.Errorf("snapshot: unsupported version %d", hdr[len(snapMagic)])
	}
	db := 0
	for {
		op := br.byte1()
		if br.err != nil {
			return br.err
		}
		var expire int64
		switch op {
		case opEOF:
			sum := br.crc.Sum64()
			var tail [8]byte
			if _, err := io.ReadFull(r, tail[:]); err != nil {
				return err
			}
			if binary.LittleEndian.Uint64(tail[:]) != sum {
				return ErrBadSnapshot
			}
			return nil
		case opSelectDB:
			db = int(br.uvarint())
			continue
		case opAux:
			name := br.str()
			data := br.bytes()
			if br.err != nil {
				return br.err
			}
			if aux != nil {
				if err := aux(name, data); err != nil {
					return err
				}
			}
			continue
		case opExpire:
			expire = br.varint()
			op = br.byte1()
		}
		key := br.str()
		v := readValue(br, op)
		if br.err != nil {
			return br.err
		}
		if v == nil {
			return ErrBadSnapshot
		}
		if err := fn(db, key, v, expire); err != nil {
			return err
		}
	}
}

func readValue(br *byteReader, t byte) any {
	switch t {
	case tString:
		return br.bytes()
	case tList:
		l := ds.NewList()
		n := br.uvarint()
		for i := uint64(0); i < n && br.err == nil; i++ {
			l.PushBack(br.bytes())
		}
		return l
	case tSet:
		s := ds.NewSet()
		n := br.uvarint()
		for i := uint64(0); i < n && br.err == nil; i++ {
			s.Add(br.str())
		}
		return s
	case tZSet:
		z := ds.NewZSet()
		n := br.uvarint()
		for i := uint64(0); i < n && br.err == nil; i++ {
			m := br.str()
			z.Add(m, br.f64())
		}
		return z
	case tHash:
		h := ds.NewHash()
		n := br.uvarint()
		for i := uint64(0); i < n && br.err == nil; i++ {
			f := br.str()
			v := br.bytes()
			at := br.varint()
			h.SetKeepTTL(f, v)
			if at != 0 {
				h.SetTTL(f, at)
			}
		}
		return h
	case tStream:
		return readStream(br)
	}
	return nil
}

func readStream(br *byteReader) *ds.Stream {
	s := ds.NewStream()
	n := br.uvarint()
	for i := uint64(0); i < n && br.err == nil; i++ {
		id := br.id()
		nf := br.uvarint()
		if nf > 1<<24 {
			br.err = ErrBadSnapshot
			return nil
		}
		f := make([][]byte, nf)
		for j := range f {
			f[j] = br.bytes()
		}
		s.Append(id, f)
	}
	s.LastID = br.id()
	s.MaxDeletedID = br.id()
	s.EntriesAdded = br.uvarint()
	ng := br.uvarint()
	for i := uint64(0); i < ng && br.err == nil; i++ {
		name := br.str()
		last := br.id()
		er := br.varint()
		g, _ := s.CreateGroup(name, last, er)
		nc := br.uvarint()
		for j := uint64(0); j < nc && br.err == nil; j++ {
			cn := br.str()
			seen := br.varint()
			active := br.varint()
			c, _ := g.Consumer(cn, seen, true)
			c.ActiveTime = active
		}
		np := br.uvarint()
		for j := uint64(0); j < np && br.err == nil; j++ {
			id := br.id()
			cn := br.str()
			dt := br.varint()
			dc := br.varint()
			c, _ := g.Consumer(cn, 0, true)
			g.AddPending(&ds.PendingEntry{ID: id, Consumer: c, DeliveryTime: dt, DeliveryCount: dc})
		}
	}
	return s
}
