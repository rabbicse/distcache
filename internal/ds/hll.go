package ds

import (
	"encoding/binary"
	"math"
)

// HyperLogLog compatible with Redis: same hash (MurmurHash64A, seed
// 0xadc83b19), same 16384 6-bit registers, same estimator. PFCOUNT returns
// exactly the values Redis returns. Values are stored as strings in the Redis
// dense format, and Redis sparse values are accepted on input.

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
			if i+1 >= len(p) {
				return nil, false
			}
			idx += (int(op&0x3f)<<8 | int(p[i+1])) + 1
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
