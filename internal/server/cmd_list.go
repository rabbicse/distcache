package server

import (
	"math"
	"strconv"
	"strings"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

// getList returns the list at key; ok=false when a WRONGTYPE was replied.
func (c *Ctx) getList(key string) (*ds.List, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, true
	}
	l, isList := e.Value.(*ds.List)
	if !isList {
		c.WrongType()
		return nil, false
	}
	return l, true
}

func pushGeneric(c *Ctx, left, onlyExisting bool) {
	key := c.Arg(1)
	l, ok := c.getList(key)
	if !ok {
		return
	}
	if l == nil {
		if onlyExisting {
			c.W.WriteInt(0)
			return
		}
		l = ds.NewList()
		c.DB.Add(key, l)
	}
	for _, v := range c.Args[2:] {
		if left {
			l.PushFront(v)
		} else {
			l.PushBack(v)
		}
	}
	c.Dirty(int64(len(c.Args) - 2))
	c.W.WriteInt(int64(l.Len()))
}

func cmdLPush(c *Ctx)  { pushGeneric(c, true, false) }
func cmdRPush(c *Ctx)  { pushGeneric(c, false, false) }
func cmdLPushX(c *Ctx) { pushGeneric(c, true, true) }
func cmdRPushX(c *Ctx) { pushGeneric(c, false, true) }

func (c *Ctx) deleteIfEmptyList(key string, l *ds.List) {
	if l.Len() == 0 {
		c.DB.Delete(key)
	}
}

func popGeneric(c *Ctx, left bool) {
	key := c.Arg(1)
	hasCount := len(c.Args) == 3
	var count int64 = 1
	if len(c.Args) > 3 {
		c.WrongArgs()
		return
	}
	if hasCount {
		n, ok := parseInt(c.Args[2])
		if !ok || n < 0 {
			c.Err(errCountNeg)
			return
		}
		count = n
	}
	l, ok := c.getList(key)
	if !ok {
		return
	}
	if l == nil {
		if hasCount {
			c.W.WriteNullArray()
		} else {
			c.W.WriteNull()
		}
		return
	}
	if hasCount && count == 0 {
		c.W.WriteArrayLen(0)
		return
	}
	if !hasCount {
		var v []byte
		if left {
			v, _ = l.PopFront()
		} else {
			v, _ = l.PopBack()
		}
		c.deleteIfEmptyList(key, l)
		c.Dirty(1)
		c.W.WriteBulk(v)
		return
	}
	if count > int64(l.Len()) {
		count = int64(l.Len())
	}
	c.W.WriteArrayLen(int(count))
	for i := int64(0); i < count; i++ {
		var v []byte
		if left {
			v, _ = l.PopFront()
		} else {
			v, _ = l.PopBack()
		}
		c.W.WriteBulk(v)
	}
	c.deleteIfEmptyList(key, l)
	c.Dirty(count)
}

func cmdLPop(c *Ctx) { popGeneric(c, true) }
func cmdRPop(c *Ctx) { popGeneric(c, false) }

func cmdLLen(c *Ctx) {
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(l.Len()))
}

func cmdLIndex(c *Ctx) {
	idx, ok := c.Int(2)
	if !ok {
		return
	}
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteNull()
		return
	}
	n := int64(l.Len())
	if idx < 0 {
		idx += n
	}
	if idx < 0 || idx >= n {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(l.Index(int(idx)))
}

func cmdLSet(c *Ctx) {
	idx, ok := c.Int(2)
	if !ok {
		return
	}
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		c.Err(errNoSuchKey)
		return
	}
	n := int64(l.Len())
	if idx < 0 {
		idx += n
	}
	if idx < 0 || idx >= n {
		c.Err(errOutOfRange)
		return
	}
	l.Set(int(idx), c.Args[3])
	c.Dirty(1)
	c.W.WriteOK()
}

// normRange converts Redis start/stop indexes; returns ok=false when empty.
func normRange(start, end, n int64) (int64, int64, bool) {
	if start < 0 {
		start += n
	}
	if end < 0 {
		end += n
	}
	if start < 0 {
		start = 0
	}
	if start > end || start >= n {
		return 0, 0, false
	}
	if end >= n {
		end = n - 1
	}
	return start, end, true
}

func cmdLRange(c *Ctx) {
	start, ok := c.Int(2)
	if !ok {
		return
	}
	end, ok := c.Int(3)
	if !ok {
		return
	}
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteArrayLen(0)
		return
	}
	s, e, ok := normRange(start, end, int64(l.Len()))
	if !ok {
		c.W.WriteArrayLen(0)
		return
	}
	c.W.WriteArrayLen(int(e - s + 1))
	for i := s; i <= e; i++ {
		c.W.WriteBulk(l.Index(int(i)))
	}
}

func cmdLTrim(c *Ctx) {
	start, ok := c.Int(2)
	if !ok {
		return
	}
	end, ok := c.Int(3)
	if !ok {
		return
	}
	key := c.Arg(1)
	l, ok := c.getList(key)
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteOK()
		return
	}
	s, e, ok := normRange(start, end, int64(l.Len()))
	if !ok {
		c.DB.Delete(key)
	} else {
		l.Trim(int(s), int(e))
		c.deleteIfEmptyList(key, l)
	}
	c.Dirty(1)
	c.W.WriteOK()
}

func cmdLRem(c *Ctx) {
	count, ok := c.Int(2)
	if !ok {
		return
	}
	key := c.Arg(1)
	l, ok := c.getList(key)
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteInt(0)
		return
	}
	target := string(c.Args[3])
	removed := 0
	if count >= 0 {
		limit := count
		removed = l.Filter(func(_ int, v []byte) bool {
			if string(v) == target && (limit == 0 || int64(removed) < limit) {
				removed++
				return false
			}
			return true
		})
	} else {
		// From the tail: find indexes to drop.
		drop := map[int]bool{}
		for i := l.Len() - 1; i >= 0 && int64(len(drop)) < -count; i-- {
			if string(l.Index(i)) == target {
				drop[i] = true
			}
		}
		removed = l.Filter(func(i int, _ []byte) bool { return !drop[i] })
	}
	if removed > 0 {
		c.deleteIfEmptyList(key, l)
		c.Dirty(int64(removed))
	}
	c.W.WriteInt(int64(removed))
}

func cmdLInsert(c *Ctx) {
	var after bool
	switch {
	case c.Is(2, "after"):
		after = true
	case c.Is(2, "before"):
	default:
		c.SyntaxErr()
		return
	}
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		c.W.WriteInt(0)
		return
	}
	pivot := string(c.Args[3])
	for i := 0; i < l.Len(); i++ {
		if string(l.Index(i)) == pivot {
			pos := i
			if after {
				pos++
			}
			l.Insert(pos, c.Args[4])
			c.Dirty(1)
			c.W.WriteInt(int64(l.Len()))
			return
		}
	}
	c.W.WriteInt(-1)
}

func cmdLPos(c *Ctx) {
	var rank int64 = 1
	var count int64 = -1
	var maxlen int64
	for i := 3; i < len(c.Args); i++ {
		if i+1 >= len(c.Args) {
			c.SyntaxErr()
			return
		}
		switch {
		case c.Is(i, "rank"):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n == 0 {
				c.Err("ERR RANK can't be zero: use 1 to start from the first match, 2 from the second ... or use negative to start from the end of the list")
				return
			}
			if n == math.MinInt64 {
				c.Err("ERR value is out of range, value must between -9223372036854775807 and 9223372036854775807")
				return
			}
			rank = n
		case c.Is(i, "count"):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				c.Err("ERR COUNT can't be negative")
				return
			}
			count = n
		case c.Is(i, "maxlen"):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				c.Err("ERR MAXLEN can't be negative")
				return
			}
			maxlen = n
		default:
			c.SyntaxErr()
			return
		}
		i++
	}
	l, ok := c.getList(c.Arg(1))
	if !ok {
		return
	}
	if l == nil {
		if count >= 0 {
			c.W.WriteArrayLen(0)
		} else {
			c.W.WriteNull()
		}
		return
	}
	target := string(c.Args[2])
	var found []int64
	n := l.Len()
	skip := rank
	if skip < 0 {
		skip = -skip
	}
	skip--
	checked := int64(0)
	want := count
	if want == 0 {
		want = math.MaxInt64
	}
	if want < 0 {
		want = 1
	}
	for k := 0; k < n; k++ {
		if maxlen > 0 && checked >= maxlen {
			break
		}
		checked++
		i := k
		if rank < 0 {
			i = n - 1 - k
		}
		if string(l.Index(i)) == target {
			if skip > 0 {
				skip--
				continue
			}
			found = append(found, int64(i))
			if int64(len(found)) >= want {
				break
			}
		}
	}
	if count < 0 {
		if len(found) == 0 {
			c.W.WriteNull()
		} else {
			c.W.WriteInt(found[0])
		}
		return
	}
	c.W.WriteArrayLen(len(found))
	for _, f := range found {
		c.W.WriteInt(f)
	}
}

func parseWhere(c *Ctx, i int) (left, ok bool) {
	switch {
	case c.Is(i, "left"):
		return true, true
	case c.Is(i, "right"):
		return false, true
	}
	c.SyntaxErr()
	return false, false
}

// lmoveCore moves one element; returns the element or nil when src empty.
// ok=false when an error reply was written.
func (c *Ctx) lmoveCore(src, dst string, fromLeft, toLeft bool) ([]byte, bool) {
	sl, ok := c.getList(src)
	if !ok {
		return nil, false
	}
	if sl == nil {
		return nil, true
	}
	dl, ok := c.getList(dst)
	if !ok {
		return nil, false
	}
	var v []byte
	if fromLeft {
		v, _ = sl.PopFront()
	} else {
		v, _ = sl.PopBack()
	}
	if dl == nil {
		if src == dst {
			dl = sl
		} else {
			dl = ds.NewList()
			c.DB.Add(dst, dl)
		}
	}
	if toLeft {
		dl.PushFront(v)
	} else {
		dl.PushBack(v)
	}
	c.deleteIfEmptyList(src, sl)
	if src == dst && sl.Len() > 0 && c.DB.Peek(src) == nil {
		c.DB.Add(src, sl)
	}
	c.Dirty(2)
	return v, true
}

func cmdLMove(c *Ctx) {
	fromLeft, ok := parseWhere(c, 3)
	if !ok {
		return
	}
	toLeft, ok := parseWhere(c, 4)
	if !ok {
		return
	}
	v, ok := c.lmoveCore(c.Arg(1), c.Arg(2), fromLeft, toLeft)
	if !ok {
		return
	}
	if v == nil {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(v)
}

func cmdRPopLPush(c *Ctx) {
	v, ok := c.lmoveCore(c.Arg(1), c.Arg(2), false, true)
	if !ok {
		return
	}
	if v == nil {
		c.W.WriteNull()
		return
	}
	c.W.WriteBulk(v)
}

func nullArrayReply(w *resp.Writer) { w.WriteNullArray() }
func nullReply(w *resp.Writer)      { w.WriteNull() }

func cmdBLMove(c *Ctx) {
	fromLeft, ok := parseWhere(c, 3)
	if !ok {
		return
	}
	toLeft, ok := parseWhere(c, 4)
	if !ok {
		return
	}
	deadline, ok := c.blockDeadline(5, false)
	if !ok {
		return
	}
	blmoveCore(c, fromLeft, toLeft, deadline)
}

func cmdBRPopLPush(c *Ctx) {
	deadline, ok := c.blockDeadline(3, false)
	if !ok {
		return
	}
	blmoveCore(c, false, true, deadline)
}

func blmoveCore(c *Ctx, fromLeft, toLeft bool, deadline int64) {
	src, dst := c.Arg(1), c.Arg(2)
	v, ok := c.lmoveCore(src, dst, fromLeft, toLeft)
	if !ok {
		return
	}
	if v == nil {
		if !c.canBlock {
			c.W.WriteNull()
			return
		}
		c.Block([]string{src}, deadline, nullReply)
		return
	}
	c.Rewrite([]byte("LMOVE"), c.Args[1], c.Args[2], []byte(sideName(fromLeft)), []byte(sideName(toLeft)))
	c.W.WriteBulk(v)
}

func sideName(left bool) string {
	if left {
		return "LEFT"
	}
	return "RIGHT"
}

// blockDeadline parses a timeout once per blocking call and caches it so a
// retried command keeps its original deadline.
func (c *Ctx) blockDeadline(i int, ms bool) (int64, bool) {
	if d, ok := c.blockState.(int64); ok {
		return d, true
	}
	d, ok := c.parseTimeout(i, ms)
	if ok {
		c.blockState = d
	}
	return d, ok
}

func cmdBLPop(c *Ctx) { bpopGeneric(c, true) }
func cmdBRPop(c *Ctx) { bpopGeneric(c, false) }

func bpopGeneric(c *Ctx, left bool) {
	deadline, ok := c.blockDeadline(len(c.Args)-1, false)
	if !ok {
		return
	}
	keys := c.Args[1 : len(c.Args)-1]
	for _, k := range keys {
		key := string(k)
		l, ok := c.getList(key)
		if !ok {
			return
		}
		if l == nil {
			continue
		}
		var v []byte
		if left {
			v, _ = l.PopFront()
		} else {
			v, _ = l.PopBack()
		}
		c.deleteIfEmptyList(key, l)
		c.Dirty(1)
		if left {
			c.Rewrite([]byte("LPOP"), k)
		} else {
			c.Rewrite([]byte("RPOP"), k)
		}
		c.keys = []string{key}
		c.W.WriteArrayLen(2)
		c.W.WriteBulk(k)
		c.W.WriteBulk(v)
		return
	}
	if !c.canBlock {
		c.W.WriteNullArray()
		return
	}
	ks := make([]string, len(keys))
	for i, k := range keys {
		ks[i] = string(k)
	}
	c.Block(ks, deadline, nullArrayReply)
}

// mpopArgs parses "numkeys key... LEFT|RIGHT [COUNT n]" starting at i.
func mpopArgs(c *Ctx, i int, whereWords [2]string) (keys []string, left bool, count int64, ok bool) {
	n, isInt := parseInt(c.Args[i])
	if !isInt || n <= 0 || n > int64(len(c.Args)-i-1) {
		if !isInt || n <= 0 {
			c.Err("ERR numkeys should be greater than 0")
		} else {
			c.SyntaxErr()
		}
		return nil, false, 0, false
	}
	for j := int64(0); j < n; j++ {
		keys = append(keys, c.Arg(i+1+int(j)))
	}
	p := i + 1 + int(n)
	if p >= len(c.Args) {
		c.SyntaxErr()
		return nil, false, 0, false
	}
	switch {
	case c.Is(p, whereWords[0]):
		left = true
	case c.Is(p, whereWords[1]):
		left = false
	default:
		c.SyntaxErr()
		return nil, false, 0, false
	}
	count = 1
	p++
	if p < len(c.Args) {
		if !c.Is(p, "count") || p+1 >= len(c.Args) || p+2 != len(c.Args) {
			c.SyntaxErr()
			return nil, false, 0, false
		}
		v, isInt := parseInt(c.Args[p+1])
		if !isInt || v <= 0 {
			c.Err("ERR count should be greater than 0")
			return nil, false, 0, false
		}
		count = v
	}
	return keys, left, count, true
}

func lmpopCore(c *Ctx, keys []string, left bool, count int64) bool {
	for _, key := range keys {
		l, ok := c.getList(key)
		if !ok {
			return true
		}
		if l == nil {
			continue
		}
		n := count
		if n > int64(l.Len()) {
			n = int64(l.Len())
		}
		c.W.WriteArrayLen(2)
		c.W.WriteBulkString(key)
		c.W.WriteArrayLen(int(n))
		for i := int64(0); i < n; i++ {
			var v []byte
			if left {
				v, _ = l.PopFront()
			} else {
				v, _ = l.PopBack()
			}
			c.W.WriteBulk(v)
		}
		c.deleteIfEmptyList(key, l)
		c.Dirty(n)
		op := "RPOP"
		if left {
			op = "LPOP"
		}
		c.RewriteStrings(op, key, strconv.FormatInt(n, 10))
		c.keys = []string{key}
		return true
	}
	return false
}

func cmdLMPop(c *Ctx) {
	keys, left, count, ok := mpopArgs(c, 1, [2]string{"left", "right"})
	if !ok {
		return
	}
	if !lmpopCore(c, keys, left, count) {
		c.W.WriteNullArray()
	}
}

func cmdBLMPop(c *Ctx) {
	deadline, ok := c.blockDeadline(1, false)
	if !ok {
		return
	}
	keys, left, count, ok := mpopArgs(c, 2, [2]string{"left", "right"})
	if !ok {
		return
	}
	if lmpopCore(c, keys, left, count) {
		return
	}
	if !c.canBlock {
		c.W.WriteNullArray()
		return
	}
	c.Block(keys, deadline, nullArrayReply)
}

func numKeysAt(i int) KeysFunc {
	return func(args [][]byte) ([]int, bool) {
		if i >= len(args) {
			return nil, false
		}
		n, ok := parseInt(args[i])
		if !ok || n <= 0 {
			return nil, false
		}
		var pos []int
		for j := 0; j < int(n) && i+1+j < len(args); j++ {
			pos = append(pos, i+1+j)
		}
		return pos, false
	}
}

func lowerArg(b []byte) string { return strings.ToLower(string(b)) }
