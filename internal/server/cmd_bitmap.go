package server

import (
	"math"
	"math/bits"
	"strconv"
	"strings"
)

const maxBitOffset = 4*1024*1024*1024 - 1 // 512MB * 8 - 1

func (c *Ctx) parseBitOffset(b []byte, hash bool, typeBits int64) (int64, bool) {
	s := string(b)
	usehash := false
	if hash && strings.HasPrefix(s, "#") {
		usehash = true
		s = s[1:]
	}
	n, ok := parseInt([]byte(s))
	if !ok || n < 0 {
		c.Err(errBitOffset)
		return 0, false
	}
	if usehash {
		n *= typeBits
	}
	if n > maxBitOffset || n < 0 {
		c.Err(errBitOffset)
		return 0, false
	}
	return n, true
}

func cmdSetBit(c *Ctx) {
	off, ok := c.parseBitOffset(c.Args[2], false, 0)
	if !ok {
		return
	}
	bitv, ok := parseInt(c.Args[3])
	if !ok || (bitv != 0 && bitv != 1) {
		c.Err(errBitValue)
		return
	}
	key := c.Arg(1)
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	byteIdx := int(off >> 3)
	nv := v
	if byteIdx >= len(v) {
		nv = make([]byte, byteIdx+1)
		copy(nv, v)
	} else if e != nil {
		nv = append([]byte(nil), v...)
	}
	mask := byte(1 << (7 - uint(off&7)))
	old := 0
	if nv[byteIdx]&mask != 0 {
		old = 1
	}
	if bitv == 1 {
		nv[byteIdx] |= mask
	} else {
		nv[byteIdx] &^= mask
	}
	if e == nil {
		c.DB.Add(key, nv)
	} else {
		e.Value = nv
	}
	c.Dirty(1)
	c.W.WriteInt(int64(old))
}

func cmdGetBit(c *Ctx) {
	off, ok := c.parseBitOffset(c.Args[2], false, 0)
	if !ok {
		return
	}
	v, _, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	byteIdx := int(off >> 3)
	if byteIdx >= len(v) {
		c.W.WriteInt(0)
		return
	}
	if v[byteIdx]&(1<<(7-uint(off&7))) != 0 {
		c.W.WriteInt(1)
	} else {
		c.W.WriteInt(0)
	}
}

// bitRange resolves BITCOUNT/BITPOS start/end in BYTE or BIT units into a
// bit interval [startBit, endBit]. ok=false means empty.
func bitRange(start, end int64, strlen int64, isBit bool) (int64, int64, bool) {
	total := strlen
	if isBit {
		total = strlen * 8
	}
	if start < 0 && end < 0 && start > end {
		return 0, 0, false
	}
	if start < 0 {
		start += total
	}
	if end < 0 {
		end += total
	}
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if end >= total {
		end = total - 1
	}
	if start > end {
		return 0, 0, false
	}
	if !isBit {
		return start * 8, end*8 + 7, true
	}
	return start, end, true
}

func cmdBitCount(c *Ctx) {
	var start, end int64
	hasRange := false
	isBit := false
	switch len(c.Args) {
	case 2:
	case 4, 5:
		var ok bool
		if start, ok = c.Int(2); !ok {
			return
		}
		if end, ok = c.Int(3); !ok {
			return
		}
		if len(c.Args) == 5 {
			switch {
			case c.Is(4, "bit"):
				isBit = true
			case c.Is(4, "byte"):
			default:
				c.SyntaxErr()
				return
			}
		}
		hasRange = true
	default:
		c.SyntaxErr()
		return
	}
	v, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	if e == nil {
		c.W.WriteInt(0)
		return
	}
	sb, eb := int64(0), int64(len(v))*8-1
	if hasRange {
		var nonEmpty bool
		sb, eb, nonEmpty = bitRange(start, end, int64(len(v)), isBit)
		if !nonEmpty {
			c.W.WriteInt(0)
			return
		}
	}
	if len(v) == 0 {
		c.W.WriteInt(0)
		return
	}
	var n int64
	for b := sb; b <= eb; {
		if b&7 == 0 && b+7 <= eb {
			n += int64(bits.OnesCount8(v[b>>3]))
			b += 8
			continue
		}
		if v[b>>3]&(1<<(7-uint(b&7))) != 0 {
			n++
		}
		b++
	}
	c.W.WriteInt(n)
}

func cmdBitPos(c *Ctx) {
	bitv, ok := parseInt(c.Args[2])
	if !ok || (bitv != 0 && bitv != 1) {
		c.Err("ERR The bit argument must be 1 or 0.")
		return
	}
	var start, end int64
	endGiven := false
	isBit := false
	if len(c.Args) > 6 {
		c.SyntaxErr()
		return
	}
	if len(c.Args) >= 4 {
		var ok bool
		if start, ok = c.Int(3); !ok {
			return
		}
	}
	if len(c.Args) >= 5 {
		var ok bool
		if end, ok = c.Int(4); !ok {
			return
		}
		endGiven = true
	}
	if len(c.Args) == 6 {
		switch {
		case c.Is(5, "bit"):
			isBit = true
		case c.Is(5, "byte"):
		default:
			c.SyntaxErr()
			return
		}
	}
	v, e, ok := c.getStr(c.Arg(1))
	if !ok {
		return
	}
	if e == nil {
		if bitv == 1 {
			c.W.WriteInt(-1)
		} else {
			c.W.WriteInt(0)
		}
		return
	}
	if !endGiven {
		if isBit {
			end = int64(len(v))*8 - 1
		} else {
			end = int64(len(v)) - 1
		}
	}
	sb, eb, nonEmpty := bitRange(start, end, int64(len(v)), isBit)
	if !nonEmpty {
		c.W.WriteInt(-1)
		return
	}
	for b := sb; b <= eb; b++ {
		set := v[b>>3]&(1<<(7-uint(b&7))) != 0
		if (bitv == 1) == set {
			c.W.WriteInt(b)
			return
		}
	}
	if bitv == 0 && !endGiven {
		// Looking for clear bits and the range is all ones: the first clear
		// bit is right after the string.
		c.W.WriteInt(eb + 1)
		return
	}
	c.W.WriteInt(-1)
}

func cmdBitOp(c *Ctx) {
	op := strings.ToLower(c.Arg(1))
	switch op {
	case "and", "or", "xor", "not", "diff", "diff1", "andor", "one":
	default:
		c.SyntaxErr()
		return
	}
	srcKeys := c.Args[3:]
	if op == "not" && len(srcKeys) != 1 {
		c.Err("ERR BITOP NOT must be called with a single source key.")
		return
	}
	if (op == "diff" || op == "diff1" || op == "andor") && len(srcKeys) < 2 {
		c.Err("ERR BITOP " + strings.ToUpper(op) + " must be called with at least two source keys.")
		return
	}
	srcs := make([][]byte, len(srcKeys))
	maxLen := 0
	for i, k := range srcKeys {
		v, _, ok := c.getStr(string(k))
		if !ok {
			return
		}
		srcs[i] = v
		if len(v) > maxLen {
			maxLen = len(v)
		}
	}
	at := func(i, j int) byte {
		if j < len(srcs[i]) {
			return srcs[i][j]
		}
		return 0
	}
	res := make([]byte, maxLen)
	for j := 0; j < maxLen; j++ {
		var b byte
		switch op {
		case "not":
			b = ^at(0, j)
		case "and":
			b = 0xff
			for i := range srcs {
				b &= at(i, j)
			}
		case "or":
			for i := range srcs {
				b |= at(i, j)
			}
		case "xor":
			for i := range srcs {
				b ^= at(i, j)
			}
		case "diff": // X & ~(Y1 | Y2 ...)
			var others byte
			for i := 1; i < len(srcs); i++ {
				others |= at(i, j)
			}
			b = at(0, j) &^ others
		case "diff1": // ~X & (Y1 | Y2 ...)
			var others byte
			for i := 1; i < len(srcs); i++ {
				others |= at(i, j)
			}
			b = ^at(0, j) & others
		case "andor": // X & (Y1 | Y2 ...)
			var others byte
			for i := 1; i < len(srcs); i++ {
				others |= at(i, j)
			}
			b = at(0, j) & others
		case "one": // bits set in exactly one source
			var seenOnce, seenMore byte
			for i := range srcs {
				x := at(i, j)
				seenMore |= seenOnce & x
				seenOnce ^= x
			}
			b = seenOnce &^ seenMore
		}
		res[j] = b
	}
	dst := c.Arg(2)
	if maxLen == 0 {
		if c.DB.Delete(dst) {
			c.Dirty(1)
		}
		c.W.WriteInt(0)
		return
	}
	c.DB.SetValue(dst, res, false)
	c.Dirty(1)
	c.W.WriteInt(int64(maxLen))
}

// ---- BITFIELD ----

type bfOp struct {
	kind     int // 0 get, 1 set, 2 incrby
	signed   bool
	bits     int64
	offset   int64
	value    int64
	overflow int // 0 wrap, 1 sat, 2 fail
}

func getUnsignedBits(p []byte, offset, nbits int64) uint64 {
	var v uint64
	for j := int64(0); j < nbits; j++ {
		byteIdx := (offset + j) >> 3
		bit := 7 - uint((offset+j)&7)
		var b uint64
		if byteIdx < int64(len(p)) {
			b = uint64(p[byteIdx]>>bit) & 1
		}
		v = v<<1 | b
	}
	return v
}

func setUnsignedBits(p []byte, offset, nbits int64, v uint64) {
	for j := int64(0); j < nbits; j++ {
		bitval := (v >> uint(nbits-1-j)) & 1
		byteIdx := (offset + j) >> 3
		bit := 7 - uint((offset+j)&7)
		if bitval == 1 {
			p[byteIdx] |= 1 << bit
		} else {
			p[byteIdx] &^= 1 << bit
		}
	}
}

func getSignedBits(p []byte, offset, nbits int64) int64 {
	u := getUnsignedBits(p, offset, nbits)
	if nbits == 64 {
		return int64(u)
	}
	if u&(1<<uint(nbits-1)) != 0 {
		u |= ^uint64(0) << uint(nbits)
	}
	return int64(u)
}

// checkSignedOverflow ports checkSignedBitfieldOverflow.
func checkSignedOverflow(value, incr int64, nbits int64, ow int) (int, int64) {
	max := int64(math.MaxInt64)
	if nbits != 64 {
		max = (int64(1) << uint(nbits-1)) - 1
	}
	min := -max - 1
	maxincr := int64(uint64(max) - uint64(value))
	minincr := min - value
	wrap := func() int64 {
		msb := uint64(1) << uint(nbits-1)
		cv := uint64(value) + uint64(incr)
		if nbits < 64 {
			mask := ^uint64(0) << uint(nbits)
			if cv&msb != 0 {
				cv |= mask
			} else {
				cv &^= mask
			}
		}
		return int64(cv)
	}
	if value > max || (nbits != 64 && incr > maxincr) || (value >= 0 && incr > 0 && incr > maxincr) {
		switch ow {
		case 0:
			return 1, wrap()
		case 1:
			return 1, max
		}
		return 1, 0
	}
	if value < min || (nbits != 64 && incr < minincr) || (value < 0 && incr < 0 && incr < minincr) {
		switch ow {
		case 0:
			return -1, wrap()
		case 1:
			return -1, min
		}
		return -1, 0
	}
	return 0, 0
}

func checkUnsignedOverflow(value uint64, incr int64, nbits int64, ow int) (int, uint64) {
	max := uint64(math.MaxUint64)
	if nbits != 64 {
		max = (uint64(1) << uint(nbits)) - 1
	}
	maxincr := int64(max - value)
	minincr := -int64(value)
	wrap := func() uint64 {
		mask := ^uint64(0) << uint(nbits)
		res := value + uint64(incr)
		return res &^ mask
	}
	if value > max || (incr > 0 && incr > maxincr) {
		switch ow {
		case 0:
			return 1, wrap()
		case 1:
			return 1, max
		}
		return 1, 0
	}
	if incr < 0 && incr < minincr {
		switch ow {
		case 0:
			return -1, wrap()
		case 1:
			return -1, 0
		}
		return -1, 0
	}
	return 0, 0
}

func cmdBitField(c *Ctx)   { bitfieldGeneric(c, false) }
func cmdBitFieldRO(c *Ctx) { bitfieldGeneric(c, true) }

func bitfieldGeneric(c *Ctx, ro bool) {
	var ops []bfOp
	ow := 0
	needWrite := false
	for i := 2; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		sub := strings.ToLower(c.Arg(i))
		switch {
		case sub == "get" && left >= 2, sub == "set" && left >= 3, sub == "incrby" && left >= 3:
			t := c.Arg(i + 1)
			if len(t) < 2 || (t[0] != 'i' && t[0] != 'u' && t[0] != 'I' && t[0] != 'U') {
				c.Err("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")
				return
			}
			nb, err := strconv.ParseInt(t[1:], 10, 64)
			signed := t[0] == 'i' || t[0] == 'I'
			if err != nil || nb < 1 || (signed && nb > 64) || (!signed && nb > 63) {
				c.Err("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")
				return
			}
			off, ok := c.parseBitOffset(c.Args[i+2], true, nb)
			if !ok {
				return
			}
			op := bfOp{signed: signed, bits: nb, offset: off, overflow: ow}
			switch sub {
			case "get":
				op.kind = 0
				i += 2
			case "set", "incrby":
				if ro {
					c.Err("ERR BITFIELD_RO only supports the GET subcommand")
					return
				}
				v, ok := c.Int(i + 3)
				if !ok {
					return
				}
				op.value = v
				op.kind = 1
				if sub == "incrby" {
					op.kind = 2
				}
				needWrite = true
				i += 3
			}
			ops = append(ops, op)
		case sub == "overflow" && left >= 1:
			if ro {
				c.Err("ERR BITFIELD_RO only supports the GET subcommand")
				return
			}
			switch strings.ToLower(c.Arg(i + 1)) {
			case "wrap":
				ow = 0
			case "sat":
				ow = 1
			case "fail":
				ow = 2
			default:
				c.Err("ERR Invalid OVERFLOW type specified")
				return
			}
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	key := c.Arg(1)
	v, e, ok := c.getStr(key)
	if !ok {
		return
	}
	buf := v
	if needWrite {
		var maxByte int64
		for _, op := range ops {
			if op.kind != 0 {
				if b := (op.offset + op.bits - 1) >> 3; b+1 > maxByte {
					maxByte = b + 1
				}
			}
		}
		if int64(len(v)) < maxByte {
			buf = make([]byte, maxByte)
			copy(buf, v)
		} else {
			buf = append([]byte(nil), v...)
		}
	}
	changes := 0
	c.W.WriteArrayLen(len(ops))
	for _, op := range ops {
		switch op.kind {
		case 0:
			if op.signed {
				c.W.WriteInt(getSignedBits(buf, op.offset, op.bits))
			} else {
				c.W.WriteInt(int64(getUnsignedBits(buf, op.offset, op.bits)))
			}
		default:
			if op.signed {
				old := getSignedBits(buf, op.offset, op.bits)
				var nv int64
				var of int
				var lim int64
				if op.kind == 2 {
					nv = old + op.value
					of, lim = checkSignedOverflow(old, op.value, op.bits, op.overflow)
				} else {
					nv = op.value
					of, lim = checkSignedOverflow(op.value, 0, op.bits, op.overflow)
				}
				if of != 0 {
					nv = lim
				}
				if of != 0 && op.overflow == 2 {
					c.W.WriteNull()
					continue
				}
				setUnsignedBits(buf, op.offset, op.bits, uint64(nv))
				changes++
				if op.kind == 2 {
					c.W.WriteInt(nv)
				} else {
					c.W.WriteInt(old)
				}
			} else {
				old := getUnsignedBits(buf, op.offset, op.bits)
				var nv uint64
				var of int
				var lim uint64
				if op.kind == 2 {
					nv = old + uint64(op.value)
					of, lim = checkUnsignedOverflow(old, op.value, op.bits, op.overflow)
				} else {
					nv = uint64(op.value)
					of, lim = checkUnsignedOverflow(uint64(op.value), 0, op.bits, op.overflow)
				}
				if of != 0 {
					nv = lim
				}
				if of != 0 && op.overflow == 2 {
					c.W.WriteNull()
					continue
				}
				setUnsignedBits(buf, op.offset, op.bits, nv)
				changes++
				if op.kind == 2 {
					c.W.WriteInt(int64(nv))
				} else {
					c.W.WriteInt(int64(old))
				}
			}
		}
	}
	if needWrite && (changes > 0 || len(buf) != len(v)) {
		if e == nil {
			c.DB.Add(key, buf)
		} else {
			e.Value = buf
		}
		c.Dirty(int64(changes) + 1)
	}
}
