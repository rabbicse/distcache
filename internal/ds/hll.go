package ds

import (
	"encoding/binary"
	"math"
)

// HyperLogLog compatible with Redis: same hash (MurmurHash64A, seed
// 0xadc83b19), same 16384 6-bit registers, same estimator. PFCOUNT returns
// exactly the values Redis returns. Values are stored as strings in Redis's
// own encodings: new HLLs start sparse and are edited opcode by opcode exactly
// like hyperloglog.c's hllSparseSet, then promoted to dense, so GET and DUMP
// see the same bytes as Redis.

const (
	hllP         = 14
	hllQ         = 64 - hllP
	hllRegisters = 1 << hllP
	hllPMask     = hllRegisters - 1
	hllBits      = 6
	hllRegMax    = (1 << hllBits) - 1
	hllHdrSize   = 16
	// HLLDenseSize is the byte size of a dense HLL string.
	HLLDenseSize = hllHdrSize + (hllRegisters*hllBits+7)/8
	hllDense     = 0
	hllSparse    = 1
	hllAlphaInf  = 0.721347520444481703680

	// HLLSparseMaxBytes is Redis's default hll-sparse-max-bytes: a sparse
	// HLL that would grow past it is promoted to dense.
	HLLSparseMaxBytes = 3000

	hllSparseValMaxValue = 32
	hllSparseValMaxLen   = 4
	hllSparseZeroMaxLen  = 64
	hllSparseXZeroMaxLen = 16384
	hllCacheInvalid      = 1 << 7 // flag in header byte 15
)

// MurmurHash64A is Redis' 64-bit MurmurHash2 variant.
func MurmurHash64A(key []byte, seed uint64) uint64 {
	const m = 0xc6a4a7935bd1e995
	const r = 47
	h := seed ^ (uint64(len(key)) * m)
	n := len(key) - len(key)&7
	for i := 0; i < n; i += 8 {
		k := binary.LittleEndian.Uint64(key[i:])
		k *= m
		k ^= k >> r
		k *= m
		h ^= k
		h *= m
	}
	rem := key[n:]
	switch len(rem) {
	case 7:
		h ^= uint64(rem[6]) << 48
		fallthrough
	case 6:
		h ^= uint64(rem[5]) << 40
		fallthrough
	case 5:
		h ^= uint64(rem[4]) << 32
		fallthrough
	case 4:
		h ^= uint64(rem[3]) << 24
		fallthrough
	case 3:
		h ^= uint64(rem[2]) << 16
		fallthrough
	case 2:
		h ^= uint64(rem[1]) << 8
		fallthrough
	case 1:
		h ^= uint64(rem[0])
		h *= m
	}
	h ^= h >> r
	h *= m
	h ^= h >> r
	return h
}

func hllPatLen(ele []byte) (int, uint8) {
	hash := MurmurHash64A(ele, 0xadc83b19)
	index := int(hash & hllPMask)
	hash >>= hllP
	hash |= uint64(1) << hllQ
	bit := uint64(1)
	count := uint8(1)
	for hash&bit == 0 {
		count++
		bit <<= 1
	}
	return index, count
}

// HLL holds decoded registers (one byte per register for speed).
type HLL struct{ Reg [hllRegisters]uint8 }

// NewHLLString returns an empty dense HLL encoded string.
func NewHLLString() []byte {
	b := make([]byte, HLLDenseSize)
	copy(b, "HYLL")
	b[4] = hllDense
	b[15] = 1 << 7 // cached cardinality invalid
	return b
}

// IsHLL validates an encoded HLL string.
func IsHLL(b []byte) bool {
	if len(b) < hllHdrSize || string(b[:4]) != "HYLL" {
		return false
	}
	switch b[4] {
	case hllDense:
		return len(b) == HLLDenseSize
	case hllSparse:
		return true
	}
	return false
}

// DecodeHLL decodes a dense or sparse HLL string; ok=false if corrupt.
func DecodeHLL(b []byte) (*HLL, bool) {
	if !IsHLL(b) {
		return nil, false
	}
	h := &HLL{}
	if b[4] == hllDense {
		p := b[hllHdrSize:]
		for i := 0; i < hllRegisters; i++ {
			byt := i * hllBits / 8
			fb := uint(i*hllBits) & 7
			b0 := uint(p[byt])
			var b1 uint
			if byt+1 < len(p) {
				b1 = uint(p[byt+1])
			}
			h.Reg[i] = uint8(((b0 >> fb) | (b1 << (8 - fb))) & hllRegMax)
		}
		return h, true
	}
	idx := 0
	p := b[hllHdrSize:]
	for i := 0; i < len(p); {
		op := p[i]
		switch {
		case op&0xc0 == 0: // ZERO
			idx += int(op&0x3f) + 1
			i++
		case op&0xc0 == 0x40: // XZERO
			// A truncated XZERO reads a zero low byte, as Redis reads the
			// sds terminator.
			var lo byte
			if i+1 < len(p) {
				lo = p[i+1]
			}
			idx += (int(op&0x3f)<<8 | int(lo)) + 1
			i += 2
		default: // VAL
			val := uint8((op>>2)&0x1f) + 1
			run := int(op&0x3) + 1
			for j := 0; j < run; j++ {
				if idx >= hllRegisters {
					return nil, false
				}
				h.Reg[idx] = val
				idx++
			}
			i++
		}
		if idx > hllRegisters {
			return nil, false
		}
	}
	if idx != hllRegisters {
		return nil, false
	}
	return h, true
}

// Encode returns the dense encoding.
func (h *HLL) Encode() []byte {
	b := make([]byte, HLLDenseSize+1)
	copy(b, "HYLL")
	b[4] = hllDense
	b[15] = 1 << 7
	p := b[hllHdrSize:]
	for i := 0; i < hllRegisters; i++ {
		v := uint(h.Reg[i])
		byt := i * hllBits / 8
		fb := uint(i*hllBits) & 7
		fb8 := 8 - fb
		p[byt] &= ^byte(hllRegMax << fb)
		p[byt] |= byte(v << fb)
		p[byt+1] &= ^byte(hllRegMax >> fb8)
		p[byt+1] |= byte(v >> fb8)
	}
	return b[:HLLDenseSize]
}

// Add inserts ele; returns true if a register changed.
func (h *HLL) Add(ele []byte) bool {
	idx, count := hllPatLen(ele)
	if h.Reg[idx] < count {
		h.Reg[idx] = count
		return true
	}
	return false
}

// Merge takes the per-register max with o.
func (h *HLL) Merge(o *HLL) {
	for i := range h.Reg {
		if o.Reg[i] > h.Reg[i] {
			h.Reg[i] = o.Reg[i]
		}
	}
}

func hllSigma(x float64) float64 {
	if x == 1 {
		return math.Inf(1)
	}
	y := 1.0
	z := x
	for {
		x *= x
		zp := z
		z += x * y
		y += y
		if zp == z {
			break
		}
	}
	return z
}

func hllTau(x float64) float64 {
	if x == 0 || x == 1 {
		return 0
	}
	y := 1.0
	z := 1 - x
	for {
		x = math.Sqrt(x)
		zp := z
		y *= 0.5
		z -= math.Pow(1-x, 2) * y
		if zp == z {
			break
		}
	}
	return z / 3
}

// Count estimates cardinality exactly as Redis' hllCount().
func (h *HLL) Count() uint64 {
	var histo [64]int
	for _, r := range h.Reg {
		histo[r]++
	}
	m := float64(hllRegisters)
	z := m * hllTau((m-float64(histo[hllQ+1]))/m)
	for j := hllQ; j >= 1; j-- {
		z += float64(histo[j])
		z *= 0.5
	}
	z += m * hllSigma(float64(histo[0])/m)
	return uint64(math.Round(hllAlphaInf * m * m / z))
}

// NewHLLSparse returns an empty HLL in Redis's createHLLObject form: a sparse
// header with a valid zero cardinality cache and one XZERO opcode covering
// every register.
func NewHLLSparse() []byte {
	b := make([]byte, hllHdrSize+2)
	copy(b, "HYLL")
	b[4] = hllSparse
	xzeroSet(b[hllHdrSize:], hllRegisters)
	return b
}

// HLLIsDense reports whether a valid HLL string uses the dense encoding.
func HLLIsDense(b []byte) bool { return b[4] == hllDense }

// HLLInvalidateCache marks the cached cardinality in the header as stale.
func HLLInvalidateCache(b []byte) { b[15] |= hllCacheInvalid }

// HLLCachedCount returns the cardinality cached in the header, if valid.
func HLLCachedCount(b []byte) (uint64, bool) {
	if b[15]&hllCacheInvalid != 0 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b[8:16]), true
}

// HLLAdd adds ele to the HLL string b (which the caller owns and which may be
// modified in place) like Redis's hllAdd. It returns the possibly
// reallocated string and 1 if a register changed, 0 if not, or -1 if b is a
// corrupt sparse HLL.
func HLLAdd(b, ele []byte) ([]byte, int) {
	idx, count := hllPatLen(ele)
	return HLLSet(b, idx, count)
}

// HLLSet raises register index to count if it is lower, with the same
// results as HLLAdd.
func HLLSet(b []byte, index int, count uint8) ([]byte, int) {
	if b[4] == hllDense {
		return b, denseSet(b[hllHdrSize:], index, count)
	}
	return sparseSet(b, index, count)
}

// HLLToDense converts a sparse HLL string to dense like hllSparseToDense,
// keeping the header (including the cardinality cache). Dense input is
// returned unchanged. ok is false if the sparse data is corrupt.
func HLLToDense(b []byte) ([]byte, bool) {
	if b[4] == hllDense {
		return b, true
	}
	h, ok := DecodeHLL(b)
	if !ok {
		return nil, false
	}
	d := h.Encode()
	copy(d[:hllHdrSize], b[:hllHdrSize])
	d[4] = hllDense
	return d, true
}

// HLLWriteDense overwrites every register of the dense HLL string b with h,
// like hllDenseCompress, keeping the header.
func HLLWriteDense(b []byte, h *HLL) {
	copy(b[hllHdrSize:], h.Encode()[hllHdrSize:])
}

// denseRegister reads register i of the dense register area p.
func denseRegister(p []byte, i int) uint8 {
	byt := i * hllBits / 8
	fb := uint(i*hllBits) & 7
	b0 := uint(p[byt])
	var b1 uint
	if byt+1 < len(p) {
		b1 = uint(p[byt+1])
	}
	return uint8(((b0 >> fb) | (b1 << (8 - fb))) & hllRegMax)
}

// denseSet is hllDenseSet: it raises register index and reports 1 if it
// changed.
func denseSet(p []byte, index int, count uint8) int {
	if denseRegister(p, index) >= count {
		return 0
	}
	v := uint(count)
	byt := index * hllBits / 8
	fb := uint(index*hllBits) & 7
	fb8 := 8 - fb
	p[byt] &= ^byte(hllRegMax << fb)
	p[byt] |= byte(v << fb)
	if byt+1 < len(p) {
		p[byt+1] &= ^byte(hllRegMax >> fb8)
		p[byt+1] |= byte(v >> fb8)
	}
	return 1
}

// Sparse opcode helpers mirroring hyperloglog.c's HLL_SPARSE_* macros.
func isZeroOp(op byte) bool  { return op&0xc0 == 0 }
func isXZeroOp(op byte) bool { return op&0xc0 == 0x40 }
func isValOp(op byte) bool   { return op&0x80 != 0 }
func valValue(op byte) uint8 { return (op>>2)&0x1f + 1 }
func valLen(op byte) int     { return int(op&0x3) + 1 }
func zeroLen(op byte) int    { return int(op&0x3f) + 1 }
func valOp(v uint8, n int) byte {
	return byte((int(v)-1)<<2|(n-1)) | 0x80
}
func xzeroSet(p []byte, n int) {
	n--
	p[0] = byte(n>>8) | 0x40
	p[1] = byte(n)
}

// xzeroLen decodes the XZERO opcode at b[p]; a missing second byte reads as
// zero, as Redis reads the sds terminator.
func xzeroLen(b []byte, p int) int {
	var lo byte
	if p+1 < len(b) {
		lo = b[p+1]
	}
	return (int(b[p]&0x3f)<<8 | int(lo)) + 1
}

// appendZeroRun appends a ZERO or XZERO opcode for n registers.
func appendZeroRun(seq []byte, n int) []byte {
	if n > hllSparseZeroMaxLen {
		var op [2]byte
		xzeroSet(op[:], n)
		return append(seq, op[:]...)
	}
	return append(seq, byte(n-1))
}

// sparseSet is a port of Redis's hllSparseSet. The resulting bytes match
// Redis's for the same sequence of updates.
func sparseSet(b []byte, index int, count uint8) ([]byte, int) {
	if count > hllSparseValMaxValue {
		return promoteSet(b, index, count)
	}
	// Step 1: locate the opcode covering index.
	end := len(b)
	p, prev := hllHdrSize, -1
	first, span := 0, 0
	for p < end {
		oplen := 1
		switch op := b[p]; {
		case isZeroOp(op):
			span = zeroLen(op)
		case isValOp(op):
			span = valLen(op)
		default:
			span = xzeroLen(b, p)
			oplen = 2
		}
		if index <= first+span-1 {
			break
		}
		prev = p
		p += oplen
		first += span
	}
	if span == 0 || p >= end {
		return b, -1
	}
	op := b[p]
	isZero, isXZero, isVal := isZeroOp(op), isXZeroOp(op), isValOp(op)
	var runlen int
	switch {
	case isZero:
		runlen = zeroLen(op)
	case isXZero:
		runlen = xzeroLen(b, p)
	default:
		runlen = valLen(op)
	}

	// Step 2: the cheap in-place cases.
	if isVal {
		if valValue(op) >= count {
			return b, 0
		}
		if runlen == 1 {
			b[p] = valOp(count, 1)
			return sparseMerge(b, prev), 1
		}
	}
	if isZero && runlen == 1 {
		b[p] = valOp(count, 1)
		return sparseMerge(b, prev), 1
	}

	// Case C: split the opcode into up to three.
	last := first + span - 1
	seq := make([]byte, 0, 5)
	if isZero || isXZero {
		if index != first {
			seq = appendZeroRun(seq, index-first)
		}
		seq = append(seq, valOp(count, 1))
		if index != last {
			seq = appendZeroRun(seq, last-index)
		}
	} else {
		cur := valValue(op)
		if index != first {
			seq = append(seq, valOp(cur, index-first))
		}
		seq = append(seq, valOp(count, 1))
		if index != last {
			seq = append(seq, valOp(cur, last-index))
		}
	}

	// Step 3: substitute the sequence, promoting if it would grow too big.
	oldlen := 1
	if isXZero {
		oldlen = 2
	}
	delta := len(seq) - oldlen
	if delta > 0 && len(b)+delta > HLLSparseMaxBytes {
		return promoteSet(b, index, count)
	}
	tail := b[min(p+oldlen, end):]
	nb := make([]byte, 0, len(b)+delta)
	nb = append(nb, b[:p]...)
	nb = append(nb, seq...)
	nb = append(nb, tail...)
	return sparseMerge(nb, prev), 1
}

// sparseMerge is Step 4 of hllSparseSet: starting at prev (or the first
// opcode) it scans up to five opcodes and merges adjacent VAL opcodes with
// the same value. It also invalidates the cardinality cache.
func sparseMerge(b []byte, prev int) []byte {
	p := prev
	if p < 0 {
		p = hllHdrSize
	}
	for scan := 5; p < len(b) && scan > 0; scan-- {
		op := b[p]
		if isXZeroOp(op) {
			p += 2
			continue
		}
		if isZeroOp(op) {
			p++
			continue
		}
		if p+1 < len(b) && isValOp(b[p+1]) {
			v := valValue(op)
			if n := valLen(op) + valLen(b[p+1]); v == valValue(b[p+1]) && n <= hllSparseValMaxLen {
				b[p+1] = valOp(v, n)
				b = append(b[:p], b[p+1:]...)
				continue
			}
		}
		p++
	}
	HLLInvalidateCache(b)
	return b
}

// promoteSet converts b to dense and then sets the register, as the promote
// label of hllSparseSet does.
func promoteSet(b []byte, index int, count uint8) ([]byte, int) {
	d, ok := HLLToDense(b)
	if !ok {
		return b, -1
	}
	return d, denseSet(d[hllHdrSize:], index, count)
}
