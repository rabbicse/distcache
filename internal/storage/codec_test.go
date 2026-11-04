package storage

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/rabbicse/distcache/internal/ds"
)

type decoded struct {
	db  int
	val any
	exp int64
}

func buildAllTypes() map[string]*Entry {
	l := ds.NewList()
	for _, v := range []string{"a", "b", "", "c"} {
		l.PushBack([]byte(v))
	}
	s := ds.NewSet()
	s.Add("x")
	s.Add("y")
	z := ds.NewZSet()
	z.Add("m1", 1.5)
	z.Add("m2", -2)
	z.Add("m3", math.Inf(1))
	h := ds.NewHash()
	h.SetKeepTTL("f1", []byte("v1"))
	h.SetKeepTTL("f2", []byte("v2"))
	h.SetTTL("f2", baseMs+5000)
	st := ds.NewStream()
	st.Append(ds.StreamID{Ms: 1, Seq: 0}, [][]byte{[]byte("a"), []byte("1")})
	st.Append(ds.StreamID{Ms: 2, Seq: 0}, [][]byte{[]byte("b"), []byte("2")})
	st.LastID = ds.StreamID{Ms: 2, Seq: 0}
	st.EntriesAdded = 2
	g, _ := st.CreateGroup("grp", ds.StreamID{Ms: 1}, 1)
	c, _ := g.Consumer("alice", 111, true)
	c.ActiveTime = 222
	g.AddPending(&ds.PendingEntry{ID: ds.StreamID{Ms: 1}, Consumer: c, DeliveryTime: 333, DeliveryCount: 4})

	mk := func(v any, exp int64) *Entry { return &Entry{Value: v, ExpireAt: exp} }
	return map[string]*Entry{
		"str":    mk([]byte("hello\x00world"), 0),
		"empty":  mk([]byte{}, baseMs+1),
		"list":   mk(l, baseMs+2),
		"set":    mk(s, 0),
		"zset":   mk(z, 0),
		"hash":   mk(h, baseMs+3),
		"stream": mk(st, 0),
	}
}

func encodeAll(t *testing.T, ents map[string]*Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	enc.WriteAux("lib", []byte("code"))
	for k, e := range ents {
		enc.WriteEntry(0, k, e)
	}
	enc.WriteEntry(3, "other", &Entry{Value: []byte("db3")})
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if enc.Written() <= 0 || enc.Err() != nil {
		t.Fatalf("Written=%d Err=%v", enc.Written(), enc.Err())
	}
	return buf.Bytes()
}

func normalize(v any) any {
	switch x := v.(type) {
	case *ds.List:
		out := make([]string, 0, x.Len())
		for _, b := range x.All() {
			out = append(out, string(b))
		}
		return out
	case *ds.Set:
		m := x.Members()
		slices.Sort(m)
		return m
	case *ds.ZSet:
		out := map[string]float64{}
		x.ForEach(func(m string, s float64) bool { out[m] = s; return true })
		return out
	case *ds.Hash:
		type fv struct {
			V   string
			TTL int64
		}
		out := map[string]fv{}
		x.ForEach(math.MinInt64, func(f string, v []byte) bool {
			at, _ := x.TTL(f, math.MinInt64)
			out[f] = fv{string(v), at}
			return true
		})
		return out
	case *ds.Stream:
		type pel struct {
			Consumer string
			Time, N  int64
		}
		type grp struct {
			Last      ds.StreamID
			Read      int64
			Consumers map[string][2]int64
			PEL       map[ds.StreamID]pel
		}
		out := struct {
			Entries   []ds.StreamEntry
			Last, Del ds.StreamID
			Added     uint64
			Groups    map[string]grp
		}{Entries: x.Entries, Last: x.LastID, Del: x.MaxDeletedID, Added: x.EntriesAdded, Groups: map[string]grp{}}
		for n, g := range x.Groups {
			gg := grp{Last: g.LastID, Read: g.EntriesRead, Consumers: map[string][2]int64{}, PEL: map[ds.StreamID]pel{}}
			for cn, c := range g.Consumers {
				gg.Consumers[cn] = [2]int64{c.SeenTime, c.ActiveTime}
			}
			for id, p := range g.PEL {
				gg.PEL[id] = pel{p.Consumer.Name, p.DeliveryTime, p.DeliveryCount}
			}
			out.Groups[n] = gg
		}
		return out
	}
	return v
}

func TestCodec_RoundTripAllTypes(t *testing.T) {
	ents := buildAllTypes()
	data := encodeAll(t, ents)

	got := map[string]decoded{}
	var auxName string
	var auxData []byte
	err := DecodeWithAux(bufio.NewReader(bytes.NewReader(data)),
		func(db int, key string, v any, exp int64) error {
			got[key] = decoded{db, v, exp}
			return nil
		},
		func(name string, d []byte) error { auxName, auxData = name, d; return nil })
	if err != nil {
		t.Fatalf("DecodeWithAux: %v", err)
	}
	if auxName != "lib" || string(auxData) != "code" {
		t.Fatalf("aux: got %q/%q, want lib/code", auxName, auxData)
	}
	if d := got["other"]; d.db != 3 || string(d.val.([]byte)) != "db3" {
		t.Fatalf("db3 key: got %+v", d)
	}
	for k, e := range ents {
		d, ok := got[k]
		if !ok {
			t.Errorf("%s: missing", k)
			continue
		}
		if d.db != 0 || d.exp != e.ExpireAt {
			t.Errorf("%s: db=%d exp=%d, want 0/%d", k, d.db, d.exp, e.ExpireAt)
		}
		if TypeName(d.val) != TypeName(e.Value) {
			t.Errorf("%s: type %s, want %s", k, TypeName(d.val), TypeName(e.Value))
			continue
		}
		if !reflect.DeepEqual(normalize(d.val), normalize(e.Value)) {
			t.Errorf("%s: value mismatch\n got  %#v\n want %#v", k, normalize(d.val), normalize(e.Value))
		}
	}
}

func TestCodec_DecodeWithoutAuxCallback(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	n := 0
	err := Decode(bufio.NewReader(bytes.NewReader(data)), func(int, string, any, int64) error { n++; return nil })
	if err != nil || n != 8 {
		t.Fatalf("n=%d err=%v, want 8/nil", n, err)
	}
}

func TestCodec_CallbackErrorsPropagate(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	boom := errors.New("boom")
	if err := Decode(bufio.NewReader(bytes.NewReader(data)), func(int, string, any, int64) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("load fn: got %v, want boom", err)
	}
	err := DecodeWithAux(bufio.NewReader(bytes.NewReader(data)),
		func(int, string, any, int64) error { return nil },
		func(string, []byte) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("aux fn: got %v, want boom", err)
	}
}

func TestCodec_RejectsBadCRC(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	data[len(data)-1] ^= 0xFF
	err := Decode(bufio.NewReader(bytes.NewReader(data)), func(int, string, any, int64) error { return nil })
	if !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("got %v, want ErrBadSnapshot", err)
	}
}

func TestCodec_RejectsBadMagic(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	data[0] = 'X'
	err := Decode(bufio.NewReader(bytes.NewReader(data)), func(int, string, any, int64) error { return nil })
	if !errors.Is(err, ErrBadSnapshot) {
		t.Fatalf("got %v, want ErrBadSnapshot", err)
	}
}

func TestCodec_RejectsBadVersionAndTruncation(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	noop := func(int, string, any, int64) error { return nil }
	bad := slices.Clone(data)
	bad[len("DCSNAP")] = 99
	if err := Decode(bufio.NewReader(bytes.NewReader(bad)), noop); err == nil {
		t.Error("bad version: got nil error")
	}
	for _, n := range []int{0, 3, 7, len(data) / 2, len(data) - 1} {
		if err := Decode(bufio.NewReader(bytes.NewReader(data[:n])), noop); err == nil {
			t.Errorf("truncated to %d: got nil error", n)
		}
	}
}

func TestCodec_RejectsCorruptBody(t *testing.T) {
	data := encodeAll(t, buildAllTypes())
	noop := func(int, string, any, int64) error { return nil }
	for i := len("DCSNAP") + 1; i < len(data); i += 7 {
		bad := slices.Clone(data)
		bad[i] ^= 0x55
		// Must never panic; a flipped body byte must be caught by decode or CRC.
		if err := Decode(bufio.NewReader(bytes.NewReader(bad)), noop); err == nil {
			t.Fatalf("corruption at offset %d went undetected", i)
		}
	}
}

func TestDumpValue_RoundTripAndRejects(t *testing.T) {
	for k, e := range buildAllTypes() {
		p := EncodeValue(e.Value)
		v, err := DecodeValue(p)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if !reflect.DeepEqual(normalize(v), normalize(e.Value)) {
			t.Errorf("%s: mismatch", k)
		}
	}
	good := EncodeValue([]byte("v"))
	tests := []struct {
		name string
		p    []byte
	}{
		{"short", []byte{1, 2, 3}},
		{"bad crc", func() []byte { b := slices.Clone(good); b[len(b)-1] ^= 1; return b }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeValue(tc.p); !errors.Is(err, ErrBadSnapshot) {
				t.Fatalf("got %v, want ErrBadSnapshot", err)
			}
		})
	}
}
