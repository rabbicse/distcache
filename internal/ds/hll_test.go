package ds

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"testing"
)

func hllWith(prefix string, n int) *HLL {
	h := &HLL{}
	for i := range n {
		h.Add([]byte(fmt.Sprintf("%s-%d", prefix, i)))
	}
	return h
}

func relErr(got, want float64) float64 { return math.Abs(got-want) / want }

func TestHLL_Count_ErrorBound(t *testing.T) {
	for _, n := range []int{1000, 10000, 100000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h := hllWith("elem", n)
			got := h.Count()
			e := relErr(float64(got), float64(n))
			t.Logf("n=%d count=%d error=%.4f%%", n, got, e*100)
			if e > 0.02 {
				t.Errorf("count %d off by %.2f%%, want within 2%%", got, e*100)
			}
		})
	}
}

func TestHLL_Count_Empty(t *testing.T) {
	if got := (&HLL{}).Count(); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestHLL_Add_ReportsChange(t *testing.T) {
	h := &HLL{}
	if !h.Add([]byte("a")) {
		t.Error("first add should change a register")
	}
	if h.Add([]byte("a")) {
		t.Error("duplicate add should not change a register")
	}
	if h.Count() != 1 {
		t.Errorf("Count got %d, want 1", h.Count())
	}
}

func TestHLL_Merge_DisjointIsUnion(t *testing.T) {
	a, b := hllWith("a", 30000), hllWith("b", 30000)
	m := &HLL{}
	m.Merge(a)
	m.Merge(b)
	if e := relErr(float64(m.Count()), 60000); e > 0.02 {
		t.Errorf("union count %d off by %.2f%%", m.Count(), e*100)
	}
	// Merging is idempotent and commutative.
	before := m.Reg
	m.Merge(a)
	if m.Reg != before {
		t.Error("merge not idempotent")
	}
	m2 := &HLL{}
	m2.Merge(b)
	m2.Merge(a)
	if m2.Reg != m.Reg {
		t.Error("merge not commutative")
	}
}

func TestHLL_EncodeDecode_RoundTrip(t *testing.T) {
	h := hllWith("x", 5000)
	enc := h.Encode()
	if len(enc) != HLLDenseSize || !IsHLL(enc) {
		t.Fatalf("encoded len=%d IsHLL=%v", len(enc), IsHLL(enc))
	}
	d, ok := DecodeHLL(enc)
	if !ok || d.Reg != h.Reg {
		t.Fatal("dense round trip lost registers")
	}
	if d.Count() != h.Count() {
		t.Errorf("count changed: %d -> %d", h.Count(), d.Count())
	}
}

// sparseEncode builds a Redis sparse representation from registers so the
// sparse decoder can be exercised (this implementation only reads sparse).
func sparseEncode(h *HLL) []byte {
	b := append([]byte("HYLL"), hllSparse, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	for i := 0; i < hllRegisters; {
		if h.Reg[i] == 0 {
			run := 0
			for i+run < hllRegisters && h.Reg[i+run] == 0 {
				run++
			}
			i += run
			for run > 0 {
				chunk := min(run, 16384)
				b = append(b, 0x40|byte((chunk-1)>>8), byte((chunk-1)&0xff))
				run -= chunk
			}
			continue
		}
		b = append(b, 0x80|(h.Reg[i]-1)<<2)
		i++
	}
	return b
}

func TestHLL_SparseToDense_PreservesCount(t *testing.T) {
	h := hllWith("s", 300) // few registers set: realistic sparse content
	sp := sparseEncode(h)
	if !IsHLL(sp) {
		t.Fatal("sparse string not recognised")
	}
	d, ok := DecodeHLL(sp)
	if !ok {
		t.Fatal("sparse decode failed")
	}
	if d.Reg != h.Reg {
		t.Fatal("sparse decode lost registers")
	}
	dense := d.Encode()
	d2, ok := DecodeHLL(dense)
	if !ok || d2.Count() != h.Count() {
		t.Errorf("promotion changed count: %d -> %d", h.Count(), d2.Count())
	}
	if len(sp) >= len(dense) {
		t.Errorf("sparse (%d) should be smaller than dense (%d)", len(sp), len(dense))
	}
}

func TestHLL_DecodeRejectsCorrupt(t *testing.T) {
	good := sparseEncode(hllWith("c", 10))
	tests := []struct {
		name string
		in   []byte
	}{
		{"short", []byte("HYLL")},
		{"bad magic", append([]byte("NOPE"), make([]byte, 20)...)},
		{"bad encoding", append([]byte("HYLL\x07"), make([]byte, 20)...)},
		{"dense wrong size", append([]byte("HYLL\x00"), make([]byte, 20)...)},
		{"sparse truncated", good[:len(good)-3]},
		{"sparse xzero cut", append(append([]byte(nil), good[:hllHdrSize]...), 0x40)},
		{"sparse overrun", append(append([]byte(nil), good...), 0x80)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := DecodeHLL(tc.in); ok {
				t.Error("corrupt input decoded")
			}
		})
	}
	if IsHLL(nil) {
		t.Error("nil is not an HLL")
	}
}

func TestNewHLLString_IsEmptyDense(t *testing.T) {
	b := NewHLLString()
	if !IsHLL(b) || len(b) != HLLDenseSize {
		t.Fatalf("len=%d IsHLL=%v", len(b), IsHLL(b))
	}
	h, ok := DecodeHLL(b)
	if !ok || h.Count() != 0 {
		t.Errorf("decode ok=%v", ok)
	}
}

func TestMurmurHash64A_Properties(t *testing.T) {
	seen := map[uint64]bool{}
	for n := range 20 { // exercise every tail length 0..7 and multiple blocks
		key := make([]byte, n)
		for i := range key {
			key[i] = byte(i + 1)
		}
		h := MurmurHash64A(key, 0xadc83b19)
		if h != MurmurHash64A(key, 0xadc83b19) {
			t.Fatal("hash not deterministic")
		}
		if seen[h] {
			t.Fatalf("collision at len %d", n)
		}
		seen[h] = true
	}
	if MurmurHash64A([]byte("a"), 1) == MurmurHash64A([]byte("a"), 2) {
		t.Error("seed ignored")
	}
}

func TestHLLSet_SparseOpcodes(t *testing.T) {
	hdr := func(b []byte) []byte { return b[hllHdrSize:] }
	tests := []struct {
		name  string
		index int
		count uint8
		want  []byte
	}{
		{"first register", 0, 1, []byte{0x80, 0x7f, 0xfe}},
		{"xzero split", 100, 3, []byte{0x40, 0x63, 0x88, 0x7f, 0x9a}},
		{"zero split", 10, 2, []byte{0x09, 0x84, 0x7f, 0xf4}},
		{"last register", hllRegisters - 1, 1, []byte{0x7f, 0xfe, 0x80}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, r := HLLSet(NewHLLSparse(), tc.index, tc.count, HLLSparseMaxBytes)
			if r != 1 || !bytes.Equal(hdr(b), tc.want) {
				t.Fatalf("got r=%d %x, want r=1 %x", r, hdr(b), tc.want)
			}
			if b[15]&hllCacheInvalid == 0 {
				t.Error("cache not invalidated")
			}
		})
	}
}

func TestHLLSet_SparseMergesAdjacentVals(t *testing.T) {
	b := NewHLLSparse()
	b, _ = HLLSet(b, 1, 2, HLLSparseMaxBytes)
	b, _ = HLLSet(b, 0, 2, HLLSparseMaxBytes)
	if want := []byte{0x85, 0x7f, 0xfd}; !bytes.Equal(b[hllHdrSize:], want) {
		t.Fatalf("got %x, want %x", b[hllHdrSize:], want)
	}
	if _, r := HLLSet(b, 0, 1, HLLSparseMaxBytes); r != 0 {
		t.Errorf("lower count: got r=%d, want 0", r)
	}
}

func TestHLLAdd_SparseMatchesRegistersAndPromotes(t *testing.T) {
	b := NewHLLSparse()
	ref := &HLL{}
	for i := range 5000 {
		ele := []byte("e" + strconv.Itoa(i))
		ref.Add(ele)
		var r int
		if b, r = HLLAdd(b, ele, HLLSparseMaxBytes); r < 0 {
			t.Fatalf("add %d: corrupt", i)
		}
		if b[4] == hllSparse && len(b) > HLLSparseMaxBytes {
			t.Fatalf("sparse length %d exceeds %d", len(b), HLLSparseMaxBytes)
		}
		if i == 10 {
			if b[4] != hllSparse {
				t.Fatal("small HLL is not sparse")
			}
			if got, ok := DecodeHLL(b); !ok || got.Reg != ref.Reg {
				t.Fatal("sparse registers differ from reference")
			}
		}
	}
	if b[4] != hllDense || len(b) != HLLDenseSize {
		t.Fatalf("got encoding %d len %d, want dense", b[4], len(b))
	}
	if got, ok := DecodeHLL(b); !ok || got.Reg != ref.Reg {
		t.Fatal("dense registers differ from reference")
	}
}

func TestHLLSet_CorruptSparse(t *testing.T) {
	b := append(NewHLLSparse()[:hllHdrSize], 0x00) // one ZERO covering register 0 only
	if _, r := HLLSet(b, 5, 1, HLLSparseMaxBytes); r != -1 {
		t.Errorf("got r=%d, want -1", r)
	}
}
