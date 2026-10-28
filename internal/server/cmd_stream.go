package server

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

const (
	errInvalidStreamID = "ERR Invalid stream ID specified as stream command argument"
	streamNodeEntries  = 100
)

func (c *Ctx) getStream(key string) (*ds.Stream, bool) {
	e := c.Lookup(key)
	if e == nil {
		return nil, true
	}
	s, ok := e.Value.(*ds.Stream)
	if !ok {
		c.WrongType()
		return nil, false
	}
	return s, true
}

// parseID parses a strict stream ID (used for XADD explicit IDs etc).
func (c *Ctx) parseID(b []byte, missingSeq uint64) (ds.StreamID, bool) {
	id, _, auto, ok := ds.ParseStreamID(string(b), missingSeq)
	if !ok || auto {
		c.Err(errInvalidStreamID)
		return id, false
	}
	return id, true
}

// parseRangeID parses XRANGE bounds: '-', '+', optional '(' exclusive.
func (c *Ctx) parseRangeID(b []byte, isStart bool) (ds.StreamID, bool) {
	s := string(b)
	if s == "-" {
		return ds.StreamID{}, true
	}
	if s == "+" {
		return ds.MaxStreamID, true
	}
	ex := false
	if strings.HasPrefix(s, "(") {
		ex = true
		s = s[1:]
	}
	missing := uint64(0)
	if !isStart {
		missing = math.MaxUint64
	}
	id, _, auto, ok := ds.ParseStreamID(s, missing)
	if !ok || auto {
		c.Err(errInvalidStreamID)
		return id, false
	}
	if ex {
		var ok2 bool
		if isStart {
			id, ok2 = id.Incr()
			if !ok2 {
				c.Err("ERR invalid start ID for the interval")
				return id, false
			}
		} else {
			id, ok2 = id.Decr()
			if !ok2 {
				c.Err("ERR invalid end ID for the interval")
				return id, false
			}
		}
	}
	return id, true
}

func writeEntry(w *resp.Writer, e *ds.StreamEntry) {
	w.WriteArrayLen(2)
	w.WriteBulkString(e.ID.String())
	w.WriteBulks(e.Fields)
}

type trimSpec struct {
	set       bool
	minid     bool
	approx    bool
	maxlen    int64
	minID     ds.StreamID
	limit     int64
	limitSet  bool
	nextIndex int
}

// parseTrim parses MAXLEN|MINID [=|~] threshold [LIMIT n] at position i.
func (c *Ctx) parseTrim(i int, t *trimSpec) (int, bool) {
	t.set = true
	t.minid = c.Is(i, "minid")
	i++
	if i < len(c.Args) && (c.Arg(i) == "~" || c.Arg(i) == "=") {
		t.approx = c.Arg(i) == "~"
		i++
	}
	if i >= len(c.Args) {
		c.SyntaxErr()
		return 0, false
	}
	if t.minid {
		id, ok := c.parseID(c.Args[i], 0)
		if !ok {
			return 0, false
		}
		t.minID = id
	} else {
		n, ok := c.Int(i)
		if !ok {
			return 0, false
		}
		if n < 0 {
			c.Err("ERR The MAXLEN argument must be >= 0.")
			return 0, false
		}
		t.maxlen = n
	}
	i++
	if i+1 < len(c.Args) && c.Is(i, "limit") {
		n, ok := c.Int(i + 1)
		if !ok {
			return 0, false
		}
		if n < 0 {
			c.Err("ERR The LIMIT argument must be >= 0.")
			return 0, false
		}
		t.limit = n
		t.limitSet = true
		i += 2
	}
	if t.limitSet && !t.approx {
		c.Err("ERR syntax error, LIMIT cannot be used without the special ~ option")
		return 0, false
	}
	return i, true
}

// applyTrim trims s and returns removed entries count.
func applyTrim(s *ds.Stream, t *trimSpec) int64 {
	var remove int
	if t.minid {
		remove = s.Seek(t.minID)
	} else if int64(s.Len()) > t.maxlen {
		remove = s.Len() - int(t.maxlen)
	}
	if remove <= 0 {
		return 0
	}
	if t.approx {
		// Emulate radix-tree node granularity: only whole nodes go away.
		remove = remove / streamNodeEntries * streamNodeEntries
		limit := t.limit
		if !t.limitSet {
			limit = 100 * streamNodeEntries
		}
		if limit > 0 && int64(remove) > limit {
			remove = int(limit) / streamNodeEntries * streamNodeEntries
		}
	}
	if remove <= 0 {
		return 0
	}
	s.TrimHead(remove)
	return int64(remove)
}

func cmdXAdd(c *Ctx) {
	key := c.Arg(1)
	i := 2
	nomk := false
	var trim trimSpec
	for i < len(c.Args) {
		switch {
		case c.Is(i, "nomkstream"):
			nomk = true
			i++
			continue
		case (c.Is(i, "maxlen") || c.Is(i, "minid")) && !trim.set:
			ni, ok := c.parseTrim(i, &trim)
			if !ok {
				return
			}
			i = ni
			continue
		}
		break
	}
	if i >= len(c.Args) {
		c.WrongArgs()
		return
	}
	idArg := c.Arg(i)
	fields := c.Args[i+1:]
	if len(fields) == 0 || len(fields)%2 != 0 {
		c.WrongArgs()
		return
	}
	var id ds.StreamID
	auto := idArg == "*"
	var seqAuto bool
	if !auto {
		var ok bool
		var seqGiven bool
		id, seqGiven, seqAuto, ok = ds.ParseStreamID(idArg, 0)
		_ = seqGiven
		if !ok {
			c.Err(errInvalidStreamID)
			return
		}
		if !seqAuto && id.IsZero() {
			c.Err("ERR The ID specified in XADD must be greater than 0-0")
			return
		}
	}
	s, ok := c.getStream(key)
	if !ok {
		return
	}
	if s == nil {
		if nomk {
			c.W.WriteNull()
			return
		}
		s = ds.NewStream()
		c.DB.Add(key, s)
	}
	last := s.LastID
	switch {
	case auto:
		ms := uint64(time.Now().UnixMilli())
		if ms > last.Ms {
			id = ds.StreamID{Ms: ms}
		} else {
			nid, ok := last.Incr()
			if !ok {
				c.Err("ERR The stream has exhausted the last possible ID, unable to add more items")
				c.cleanupEmptyStream(key, s)
				return
			}
			id = nid
		}
	case seqAuto:
		if id.Ms == last.Ms {
			if last.Seq == math.MaxUint64 {
				c.Err("ERR The ID specified in XADD is equal or smaller than the target stream top item")
				c.cleanupEmptyStream(key, s)
				return
			}
			id.Seq = last.Seq + 1
		} else if id.Ms > last.Ms {
			id.Seq = 0
		} else {
			c.Err("ERR The ID specified in XADD is equal or smaller than the target stream top item")
			c.cleanupEmptyStream(key, s)
			return
		}
	default:
		if id.Cmp(last) <= 0 {
			c.Err("ERR The ID specified in XADD is equal or smaller than the target stream top item")
			c.cleanupEmptyStream(key, s)
			return
		}
	}
	fcopy := make([][]byte, len(fields))
	copy(fcopy, fields)
	s.Append(id, fcopy)
	c.Dirty(1)
	idStr := id.String()
	prop := [][]byte{[]byte("XADD"), c.Args[1], []byte(idStr)}
	prop = append(prop, fields...)
	c.Rewrite(prop...)
	if trim.set {
		if applyTrim(s, &trim) > 0 {
			c.Dirty(1)
		}
		if s.Len() > 0 {
			c.RewriteStrings("XTRIM", key, "MINID", "=", s.FirstID().String())
		} else {
			c.RewriteStrings("XTRIM", key, "MAXLEN", "=", "0")
		}
	}
	c.W.WriteBulkString(idStr)
}

func (c *Ctx) cleanupEmptyStream(key string, s *ds.Stream) {
	if s.Len() == 0 && s.EntriesAdded == 0 && len(s.Groups) == 0 {
		c.DB.Delete(key)
	}
}

func cmdXLen(c *Ctx) {
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteInt(0)
		return
	}
	c.W.WriteInt(int64(s.Len()))
}

func xrangeGeneric(c *Ctx, rev bool) {
	startArg, endArg := c.Args[2], c.Args[3]
	if rev {
		startArg, endArg = endArg, startArg
	}
	start, ok := c.parseRangeID(startArg, true)
	if !ok {
		return
	}
	end, ok := c.parseRangeID(endArg, false)
	if !ok {
		return
	}
	count := int64(-1)
	for i := 4; i < len(c.Args); i++ {
		if c.Is(i, "count") && i+1 < len(c.Args) {
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				n = 0
			}
			count = n
			i++
		} else {
			c.SyntaxErr()
			return
		}
	}
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteArrayLen(0)
		return
	}
	if count == 0 {
		c.W.WriteNullArray()
		return
	}
	ents := streamRange(s, start, end, count, rev)
	c.W.WriteArrayLen(len(ents))
	for i := range ents {
		writeEntry(c.W, &ents[i])
	}
}

func streamRange(s *ds.Stream, start, end ds.StreamID, count int64, rev bool) []ds.StreamEntry {
	if start.Cmp(end) > 0 {
		return nil
	}
	lo := s.Seek(start)
	hi := s.Seek(end)
	if hi < s.Len() && s.Entries[hi].ID == end {
		hi++
	}
	if lo >= hi {
		return nil
	}
	var out []ds.StreamEntry
	if rev {
		for i := hi - 1; i >= lo && (count < 0 || int64(len(out)) < count); i-- {
			out = append(out, s.Entries[i])
		}
	} else {
		for i := lo; i < hi && (count < 0 || int64(len(out)) < count); i++ {
			out = append(out, s.Entries[i])
		}
	}
	return out
}

func cmdXRange(c *Ctx)    { xrangeGeneric(c, false) }
func cmdXRevRange(c *Ctx) { xrangeGeneric(c, true) }

func cmdXDel(c *Ctx) {
	ids := make([]ds.StreamID, 0, len(c.Args)-2)
	for _, a := range c.Args[2:] {
		id, ok := c.parseID(a, 0)
		if !ok {
			return
		}
		ids = append(ids, id)
	}
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteInt(0)
		return
	}
	var n int64
	for _, id := range ids {
		if s.Delete(id) {
			n++
		}
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdXTrim(c *Ctx) {
	if !c.Is(2, "maxlen") && !c.Is(2, "minid") {
		c.SyntaxErr()
		return
	}
	var t trimSpec
	i, ok := c.parseTrim(2, &t)
	if !ok {
		return
	}
	if i != len(c.Args) {
		c.SyntaxErr()
		return
	}
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.W.WriteInt(0)
		return
	}
	n := applyTrim(s, &t)
	if n > 0 {
		c.Dirty(n)
		if s.Len() > 0 {
			c.RewriteStrings("XTRIM", c.Arg(1), "MINID", "=", s.FirstID().String())
		} else {
			c.RewriteStrings("XTRIM", c.Arg(1), "MAXLEN", "=", "0")
		}
	}
	c.W.WriteInt(n)
}

func cmdXSetID(c *Ctx) {
	id, ok := c.parseID(c.Args[2], 0)
	if !ok {
		return
	}
	var entriesAdded int64 = -1
	var maxDel ds.StreamID
	maxDelSet := false
	for i := 3; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "entriesadded") && i+1 < len(c.Args):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				c.Err("ERR entries_added must be positive")
				return
			}
			entriesAdded = n
			i++
		case c.Is(i, "maxdeletedid") && i+1 < len(c.Args):
			d, ok := c.parseID(c.Args[i+1], 0)
			if !ok {
				return
			}
			if id.Cmp(d) < 0 {
				c.Err("ERR The ID specified in XSETID is smaller than the provided max_deleted_entry_id")
				return
			}
			maxDel, maxDelSet = d, true
			i++
		default:
			c.SyntaxErr()
			return
		}
	}
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil {
		c.Err(errNoSuchKey)
		return
	}
	if entriesAdded >= 0 && uint64(s.Len()) > uint64(entriesAdded) {
		c.Err("ERR The entries_added specified in XSETID is smaller than the target stream length")
		return
	}
	if s.Len() > 0 && id.Cmp(s.LastID) < 0 {
		c.Err("ERR The ID specified in XSETID is smaller than the target stream top item")
		return
	}
	s.LastID = id
	if entriesAdded >= 0 {
		s.EntriesAdded = uint64(entriesAdded)
	}
	if maxDelSet {
		s.MaxDeletedID = maxDel
	}
	c.Dirty(1)
	c.W.WriteOK()
}

// ---- XREAD / XREADGROUP ----

type xreadState struct {
	ids []ds.StreamID
}

func xreadKeys(args [][]byte) ([]int, bool) {
	for i := 1; i < len(args); i++ {
		if strings.EqualFold(string(args[i]), "streams") {
			rest := len(args) - i - 1
			if rest%2 != 0 {
				return nil, false
			}
			var pos []int
			for j := 0; j < rest/2; j++ {
				pos = append(pos, i+1+j)
			}
			return pos, false
		}
	}
	return nil, false
}

func cmdXRead(c *Ctx)      { xreadGeneric(c, false) }
func cmdXReadGroup(c *Ctx) { xreadGeneric(c, true) }

func xreadGeneric(c *Ctx, group bool) {
	count := int64(0)
	blockMs := int64(-1)
	noack := false
	var groupName, consumer string
	streamsPos := -1
	for i := 1; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case c.Is(i, "count") && left >= 1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 0 {
				n = 0
			}
			count = n
			i++
		case c.Is(i, "block") && left >= 1:
			n, ok := parseInt(c.Args[i+1])
			if !ok {
				c.Err("ERR timeout is not an integer or out of range")
				return
			}
			if n < 0 {
				c.Err(errNegTimeout)
				return
			}
			blockMs = n
			i++
		case c.Is(i, "streams") && left >= 1:
			streamsPos = i
			i = len(c.Args)
		case group && c.Is(i, "group") && left >= 2:
			groupName = c.Arg(i + 1)
			consumer = c.Arg(i + 2)
			i += 2
		case group && c.Is(i, "noack"):
			noack = true
		default:
			c.SyntaxErr()
			return
		}
	}
	if streamsPos < 0 {
		c.SyntaxErr()
		return
	}
	rest := len(c.Args) - streamsPos - 1
	name := "xread"
	if group {
		name = "xreadgroup"
	}
	if rest%2 != 0 {
		c.Err("ERR Unbalanced '" + name + "' list of streams: for each stream key an ID or '$' must be specified.")
		return
	}
	if group && groupName == "" {
		c.Err("ERR Missing GROUP option for XREADGROUP")
		return
	}
	n := rest / 2
	keys := argStrings(c.Args[streamsPos+1 : streamsPos+1+n])
	idArgs := c.Args[streamsPos+1+n:]

	// Resolve IDs once (so '$' keeps its meaning across blocking retries).
	st, _ := c.blockState.(*xreadState)
	var deadline int64
	if st == nil {
		st = &xreadState{ids: make([]ds.StreamID, n)}
		for j, a := range idArgs {
			as := string(a)
			s, ok := c.getStream(keys[j])
			if !ok {
				return
			}
			switch {
			case as == "$":
				if group {
					c.Err("ERR The $ ID is meaningless in the context of XREADGROUP: you want to read the history of this consumer by specifying a proper ID, or use the > ID to get new messages. The $ ID would just return an empty result set.")
					return
				}
				if s != nil {
					st.ids[j] = s.LastID
				}
			case as == ">":
				if !group {
					c.Err("ERR The > ID can be specified only when calling XREADGROUP using the GROUP <group> <consumer> option.")
					return
				}
				st.ids[j] = ds.MaxStreamID // marker
			case as == "+" && !group:
				if s != nil && s.Len() > 0 {
					prev, _ := s.Entries[s.Len()-1].ID.Decr()
					st.ids[j] = prev
				} else if s != nil {
					st.ids[j] = s.LastID
				}
			default:
				id, ok := c.parseID(a, 0)
				if !ok {
					return
				}
				st.ids[j] = id
			}
			if group {
				if s == nil || s.Groups[groupName] == nil {
					c.Err("NOGROUP No such key '" + keys[j] + "' or consumer group '" + groupName + "' in XREADGROUP with GROUP option")
					return
				}
			}
		}
		if blockMs > 0 {
			deadline = time.Now().UnixMilli() + blockMs
		}
		c.blockState = st
		c.blockDeadlineMs = deadline
	} else {
		deadline = c.blockDeadlineMs
	}

	type result struct {
		key  string
		ents []ds.StreamEntry
		nils []bool
	}
	var results []result
	for j, key := range keys {
		s, ok := c.getStream(key)
		if !ok {
			return
		}
		if group && (s == nil || s.Groups[groupName] == nil) {
			c.Err("NOGROUP No such key '" + key + "' or consumer group '" + groupName + "' in XREADGROUP with GROUP option")
			return
		}
		if s == nil {
			continue
		}
		if !group {
			start, ok := st.ids[j].Incr()
			if !ok {
				continue
			}
			ents := streamRange(s, start, ds.MaxStreamID, nonZero(count), false)
			if len(ents) > 0 {
				results = append(results, result{key: key, ents: ents})
			}
			continue
		}
		g := s.Groups[groupName]
		now := time.Now().UnixMilli()
		cons, _ := g.Consumer(consumer, now, true)
		cons.SeenTime = now
		if st.ids[j] == ds.MaxStreamID {
			start, _ := g.LastID.Incr()
			ents := streamRange(s, start, ds.MaxStreamID, nonZero(count), false)
			if len(ents) == 0 {
				continue
			}
			cons.ActiveTime = now
			var claims [][]byte
			for _, e := range ents {
				if e.ID.Cmp(g.LastID) > 0 {
					if g.EntriesRead != ds.InvalidEntriesRead && !s.RangeHasTombstones(&e.ID, nil) {
						g.EntriesRead++
					} else if s.EntriesAdded > 0 {
						g.EntriesRead = s.EstimateDistance(e.ID)
					}
					g.LastID = e.ID
				}
				if !noack {
					if old, exists := g.PEL[e.ID]; exists {
						delete(old.Consumer.PEL, e.ID)
						old.Consumer = cons
						old.DeliveryTime = now
						old.DeliveryCount = 1
						cons.PEL[e.ID] = old
					} else {
						g.AddPending(&ds.PendingEntry{ID: e.ID, Consumer: cons, DeliveryTime: now, DeliveryCount: 1})
					}
					claims = append(claims, []byte(e.ID.String()))
				}
			}
			c.Dirty(1)
			for _, id := range claims {
				c.Rewrite([]byte("XCLAIM"), []byte(key), []byte(groupName), []byte(consumer), []byte("0"), id,
					[]byte("TIME"), formatInt(now), []byte("RETRYCOUNT"), []byte("1"), []byte("FORCE"),
					[]byte("JUSTID"), []byte("LASTID"), []byte(g.LastID.String()))
			}
			c.RewriteStrings("XGROUP", "SETID", key, groupName, g.LastID.String(), "ENTRIESREAD", strconv.FormatInt(g.EntriesRead, 10))
			results = append(results, result{key: key, ents: ents})
			continue
		}
		// History of this consumer's PEL from the given ID.
		var ents []ds.StreamEntry
		var nils []bool
		g.PendingRange(st.ids[j], ds.MaxStreamID, func(pe *ds.PendingEntry) bool {
			if pe.Consumer != cons {
				return true
			}
			if count > 0 && int64(len(ents)) >= count {
				return false
			}
			if e, ok := s.Get(pe.ID); ok {
				ents = append(ents, *e)
				nils = append(nils, false)
			} else {
				ents = append(ents, ds.StreamEntry{ID: pe.ID})
				nils = append(nils, true)
			}
			pe.DeliveryCount++
			pe.DeliveryTime = now
			return true
		})
		results = append(results, result{key: key, ents: ents, nils: nils})
	}
	if len(results) == 0 {
		if blockMs >= 0 && c.canBlock {
			// XREADGROUP with explicit IDs never blocks.
			canBlockHere := true
			if group {
				for _, id := range st.ids {
					if id != ds.MaxStreamID {
						canBlockHere = false
					}
				}
			}
			if canBlockHere {
				c.Block(keys, deadline, nullArrayReply)
				return
			}
		}
		c.W.WriteNullArray()
		return
	}
	if c.W.Proto >= 3 {
		c.W.WriteMapLen(len(results))
	} else {
		c.W.WriteArrayLen(len(results))
	}
	for _, r := range results {
		if c.W.Proto < 3 {
			c.W.WriteArrayLen(2)
		}
		c.W.WriteBulkString(r.key)
		c.W.WriteArrayLen(len(r.ents))
		for i := range r.ents {
			if r.nils != nil && r.nils[i] {
				c.W.WriteArrayLen(2)
				c.W.WriteBulkString(r.ents[i].ID.String())
				c.W.WriteNullArray()
				continue
			}
			writeEntry(c.W, &r.ents[i])
		}
	}
}

func nonZero(n int64) int64 {
	if n == 0 {
		return -1
	}
	return n
}

// ---- consumer groups ----

func (c *Ctx) getGroup(key, group, cmd string) (*ds.Stream, *ds.ConsumerGroup, bool) {
	s, ok := c.getStream(key)
	if !ok {
		return nil, nil, false
	}
	if s == nil || s.Groups[group] == nil {
		c.Err("NOGROUP No such key '" + key + "' or consumer group '" + group + "'" + cmd)
		return nil, nil, false
	}
	return s, s.Groups[group], true
}

func cmdXGroup(c *Ctx) {
	sub := strings.ToLower(c.Arg(1))
	if sub == "help" {
		c.writeHelp("XGROUP", []string{
			"CREATE <key> <groupname> <id|$> [option]", "    Create a new consumer group. Options are:",
			"    * MKSTREAM", "      Create the empty stream if it does not exist.",
			"    * ENTRIESREAD entries_read", "      Set the group's entries_read counter (internal use).",
			"CREATECONSUMER <key> <groupname> <consumer>", "    Create a new consumer in the specified group.",
			"DELCONSUMER <key> <groupname> <consumer>", "    Remove the specified consumer.",
			"DESTROY <key> <groupname>", "    Remove the specified group.",
			"SETID <key> <groupname> <id|$> [ENTRIESREAD entries_read]", "    Set the current group ID and entries_read counter.",
		})
		return
	}
	if len(c.Args) < 4 {
		c.WrongSubArgs()
		return
	}
	key, group := c.Arg(2), c.Arg(3)
	switch sub {
	case "create":
		if len(c.Args) < 5 {
			c.WrongSubArgs()
			return
		}
		mk := false
		var er int64 = ds.InvalidEntriesRead
		for i := 5; i < len(c.Args); i++ {
			switch {
			case c.Is(i, "mkstream"):
				mk = true
			case c.Is(i, "entriesread") && i+1 < len(c.Args):
				n, ok := c.Int(i + 1)
				if !ok {
					return
				}
				if n < 0 && n != ds.InvalidEntriesRead {
					c.Err("ERR value for ENTRIESREAD must be positive or -1")
					return
				}
				er = n
				i++
			default:
				c.SyntaxErr()
				return
			}
		}
		s, ok := c.getStream(key)
		if !ok {
			return
		}
		var id ds.StreamID
		if c.Arg(4) == "$" {
			if s != nil {
				id = s.LastID
			}
		} else {
			parsed, ok := c.parseID(c.Args[4], 0)
			if !ok {
				return
			}
			id = parsed
		}
		if s == nil {
			if !mk {
				c.Err("ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.")
				return
			}
			s = ds.NewStream()
			c.DB.Add(key, s)
		}
		if _, ok := s.CreateGroup(group, id, er); !ok {
			c.Err("BUSYGROUP Consumer Group name already exists")
			return
		}
		c.Dirty(1)
		c.W.WriteOK()
	case "setid":
		if len(c.Args) != 5 && len(c.Args) != 7 {
			c.WrongSubArgs()
			return
		}
		s, g, ok := c.getGroup(key, group, "")
		if !ok {
			return
		}
		var id ds.StreamID
		if c.Arg(4) == "$" {
			id = s.LastID
		} else {
			parsed, ok := c.parseID(c.Args[4], 0)
			if !ok {
				return
			}
			id = parsed
		}
		var er int64 = ds.InvalidEntriesRead
		if len(c.Args) == 7 {
			if !c.Is(5, "entriesread") {
				c.SyntaxErr()
				return
			}
			n, ok := c.Int(6)
			if !ok {
				return
			}
			er = n
		}
		g.LastID = id
		g.EntriesRead = er
		c.Dirty(1)
		c.W.WriteOK()
	case "destroy":
		if len(c.Args) != 4 {
			c.WrongSubArgs()
			return
		}
		s, ok := c.getStream(key)
		if !ok {
			return
		}
		if s == nil {
			c.Err("ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.")
			return
		}
		if s.Groups[group] == nil {
			c.W.WriteInt(0)
			return
		}
		delete(s.Groups, group)
		c.Dirty(1)
		c.W.WriteInt(1)
	case "createconsumer":
		if len(c.Args) != 5 {
			c.WrongSubArgs()
			return
		}
		_, g, ok := c.getGroup(key, group, "")
		if !ok {
			return
		}
		if _, created := g.Consumer(c.Arg(4), time.Now().UnixMilli(), true); created {
			c.Dirty(1)
			c.W.WriteInt(1)
		} else {
			c.W.WriteInt(0)
		}
	case "delconsumer":
		if len(c.Args) != 5 {
			c.WrongSubArgs()
			return
		}
		_, g, ok := c.getGroup(key, group, "")
		if !ok {
			return
		}
		cons, _ := g.Consumer(c.Arg(4), 0, false)
		if cons == nil {
			c.W.WriteInt(0)
			return
		}
		n := len(cons.PEL)
		for id := range cons.PEL {
			g.RemovePending(id)
		}
		delete(g.Consumers, cons.Name)
		c.Dirty(1)
		c.W.WriteInt(int64(n))
	default:
		c.UnknownSub()
	}
}

func cmdXAck(c *Ctx) {
	ids := make([]ds.StreamID, 0, len(c.Args)-3)
	for _, a := range c.Args[3:] {
		id, ok := c.parseID(a, 0)
		if !ok {
			return
		}
		ids = append(ids, id)
	}
	s, ok := c.getStream(c.Arg(1))
	if !ok {
		return
	}
	if s == nil || s.Groups[c.Arg(2)] == nil {
		c.W.WriteInt(0)
		return
	}
	g := s.Groups[c.Arg(2)]
	var n int64
	for _, id := range ids {
		if g.RemovePending(id) {
			n++
		}
	}
	c.Dirty(n)
	c.W.WriteInt(n)
}

func cmdXPending(c *Ctx) {
	key, group := c.Arg(1), c.Arg(2)
	extended := len(c.Args) > 3
	var minIdle int64
	var start, end ds.StreamID
	var count int64
	var consumer string
	if extended {
		i := 3
		if c.Is(i, "idle") {
			if i+1 >= len(c.Args) {
				c.SyntaxErr()
				return
			}
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			minIdle = n
			i += 2
		}
		if len(c.Args)-i < 3 || len(c.Args)-i > 4 {
			c.SyntaxErr()
			return
		}
		var ok bool
		if start, ok = c.parseRangeID(c.Args[i], true); !ok {
			return
		}
		if end, ok = c.parseRangeID(c.Args[i+1], false); !ok {
			return
		}
		if count, ok = c.Int(i + 2); !ok {
			return
		}
		if count < 0 {
			count = 0
		}
		if len(c.Args)-i == 4 {
			consumer = c.Arg(i + 3)
		}
	}
	_, g, ok := c.getGroup(key, group, "")
	if !ok {
		return
	}
	now := time.Now().UnixMilli()
	if !extended {
		ids := g.PendingIDs()
		c.W.WriteArrayLen(4)
		c.W.WriteInt(int64(len(ids)))
		if len(ids) == 0 {
			c.W.WriteNull()
			c.W.WriteNull()
			c.W.WriteNullArray()
			return
		}
		c.W.WriteBulkString(ids[0].String())
		c.W.WriteBulkString(ids[len(ids)-1].String())
		names := g.ConsumerNames()
		var active []string
		for _, n := range names {
			if len(g.Consumers[n].PEL) > 0 {
				active = append(active, n)
			}
		}
		c.W.WriteArrayLen(len(active))
		for _, n := range active {
			c.W.WriteArrayLen(2)
			c.W.WriteBulkString(n)
			c.W.WriteBulkString(strconv.Itoa(len(g.Consumers[n].PEL)))
		}
		return
	}
	var out []*ds.PendingEntry
	g.PendingRange(start, end, func(pe *ds.PendingEntry) bool {
		if int64(len(out)) >= count {
			return false
		}
		if consumer != "" && pe.Consumer.Name != consumer {
			return true
		}
		if minIdle > 0 && now-pe.DeliveryTime < minIdle {
			return true
		}
		out = append(out, pe)
		return true
	})
	c.W.WriteArrayLen(len(out))
	for _, pe := range out {
		c.W.WriteArrayLen(4)
		c.W.WriteBulkString(pe.ID.String())
		c.W.WriteBulkString(pe.Consumer.Name)
		c.W.WriteInt(now - pe.DeliveryTime)
		c.W.WriteInt(pe.DeliveryCount)
	}
}

func cmdXClaim(c *Ctx) {
	key, group, consumer := c.Arg(1), c.Arg(2), c.Arg(3)
	minIdle, ok := c.Int(4)
	if !ok {
		return
	}
	if minIdle < 0 {
		minIdle = 0
	}
	var ids []ds.StreamID
	i := 5
	for ; i < len(c.Args); i++ {
		id, _, auto, ok := ds.ParseStreamID(c.Arg(i), 0)
		if !ok || auto {
			break
		}
		ids = append(ids, id)
	}
	now := time.Now().UnixMilli()
	deliveryTime := int64(-1)
	retry := int64(-1)
	force, justID := false, false
	var lastID ds.StreamID
	lastSet := false
	for ; i < len(c.Args); i++ {
		left := len(c.Args) - i - 1
		switch {
		case c.Is(i, "force"):
			force = true
		case c.Is(i, "justid"):
			justID = true
		case c.Is(i, "idle") && left >= 1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			deliveryTime = now - n
			i++
		case c.Is(i, "time") && left >= 1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			deliveryTime = n
			i++
		case c.Is(i, "retrycount") && left >= 1:
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			retry = n
			i++
		case c.Is(i, "lastid") && left >= 1:
			id, ok := c.parseID(c.Args[i+1], 0)
			if !ok {
				return
			}
			lastID, lastSet = id, true
			i++
		default:
			c.Err("ERR Unrecognized XCLAIM option '" + c.Arg(i) + "'")
			return
		}
	}
	if deliveryTime != -1 {
		if deliveryTime > now {
			deliveryTime = now
		} else if deliveryTime < 0 {
			deliveryTime = 0
		}
	}
	s, g, ok := c.getGroup(key, group, "")
	if !ok {
		return
	}
	if lastSet && lastID.Cmp(g.LastID) > 0 {
		g.LastID = lastID
	}
	cons, _ := g.Consumer(consumer, now, true)
	cons.SeenTime = now
	var claimed []ds.StreamEntry
	var claimedIDs []ds.StreamID
	for _, id := range ids {
		pe := g.PEL[id]
		entry, exists := s.Get(id)
		if pe == nil && force && exists {
			pe = &ds.PendingEntry{ID: id, Consumer: cons, DeliveryTime: now}
			g.AddPending(pe)
		}
		if pe == nil {
			continue
		}
		if !exists {
			g.RemovePending(id)
			continue
		}
		if minIdle > 0 && now-pe.DeliveryTime < minIdle {
			continue
		}
		if pe.Consumer != cons {
			delete(pe.Consumer.PEL, id)
			pe.Consumer = cons
			cons.PEL[id] = pe
		}
		if deliveryTime != -1 {
			pe.DeliveryTime = deliveryTime
		} else {
			pe.DeliveryTime = now
		}
		if retry != -1 {
			pe.DeliveryCount = retry
		} else if !justID {
			pe.DeliveryCount++
		}
		cons.ActiveTime = now
		claimedIDs = append(claimedIDs, id)
		claimed = append(claimed, *entry)
	}
	c.Dirty(1)
	c.W.WriteArrayLen(len(claimedIDs))
	for k, id := range claimedIDs {
		if justID {
			c.W.WriteBulkString(id.String())
		} else {
			writeEntry(c.W, &claimed[k])
		}
	}
}

func cmdXAutoClaim(c *Ctx) {
	key, group, consumer := c.Arg(1), c.Arg(2), c.Arg(3)
	minIdle, ok := c.Int(4)
	if !ok {
		return
	}
	if minIdle < 0 {
		minIdle = 0
	}
	start, ok := c.parseRangeID(c.Args[5], true)
	if !ok {
		return
	}
	count := int64(100)
	justID := false
	for i := 6; i < len(c.Args); i++ {
		switch {
		case c.Is(i, "count") && i+1 < len(c.Args):
			n, ok := c.Int(i + 1)
			if !ok {
				return
			}
			if n < 1 || n > math.MaxInt64/10 {
				c.Err("ERR COUNT must be > 0")
				return
			}
			count = n
			i++
		case c.Is(i, "justid"):
			justID = true
		default:
			c.SyntaxErr()
			return
		}
	}
	s, g, ok := c.getGroup(key, group, "")
	if !ok {
		return
	}
	now := time.Now().UnixMilli()
	cons, _ := g.Consumer(consumer, now, true)
	cons.SeenTime = now
	var claimed []ds.StreamEntry
	var claimedIDs []ds.StreamID
	var deleted []ds.StreamID
	next := ds.StreamID{}
	attempts := count * 10
	var visited []ds.StreamID
	g.PendingRange(start, ds.MaxStreamID, func(pe *ds.PendingEntry) bool {
		if attempts <= 0 || int64(len(claimedIDs)) >= count {
			next = pe.ID
			return false
		}
		attempts--
		visited = append(visited, pe.ID)
		return true
	})
	for _, id := range visited {
		pe := g.PEL[id]
		entry, exists := s.Get(id)
		if !exists {
			g.RemovePending(id)
			deleted = append(deleted, id)
			continue
		}
		if minIdle > 0 && now-pe.DeliveryTime < minIdle {
			continue
		}
		if pe.Consumer != cons {
			delete(pe.Consumer.PEL, id)
			pe.Consumer = cons
			cons.PEL[id] = pe
		}
		pe.DeliveryTime = now
		if !justID {
			pe.DeliveryCount++
		}
		cons.ActiveTime = now
		claimedIDs = append(claimedIDs, id)
		claimed = append(claimed, *entry)
	}
	c.Dirty(1)
	c.W.WriteArrayLen(3)
	c.W.WriteBulkString(next.String())
	c.W.WriteArrayLen(len(claimedIDs))
	for k, id := range claimedIDs {
		if justID {
			c.W.WriteBulkString(id.String())
		} else {
			writeEntry(c.W, &claimed[k])
		}
	}
	c.W.WriteArrayLen(len(deleted))
	for _, id := range deleted {
		c.W.WriteBulkString(id.String())
	}
}

func cmdXInfo(c *Ctx) {
	sub := strings.ToLower(c.Arg(1))
	if sub == "help" {
		c.writeHelp("XINFO", []string{
			"CONSUMERS <key> <groupname>", "    Show consumers of <groupname>.",
			"GROUPS <key>", "    Show the stream consumer groups.",
			"STREAM <key> [FULL [COUNT <count>]", "    Show information about the stream.",
		})
		return
	}
	if len(c.Args) < 3 {
		c.WrongSubArgs()
		return
	}
	key := c.Arg(2)
	s, ok := c.getStream(key)
	if !ok {
		return
	}
	if s == nil {
		c.Err(errNoSuchKey)
		return
	}
	now := time.Now().UnixMilli()
	switch sub {
	case "stream":
		full := len(c.Args) >= 4 && c.Is(3, "full")
		if len(c.Args) >= 4 && !full {
			c.SyntaxErr()
			return
		}
		if !full {
			c.W.WriteMapLen(10)
			c.W.WriteBulkString("length")
			c.W.WriteInt(int64(s.Len()))
			c.W.WriteBulkString("radix-tree-keys")
			c.W.WriteInt(int64((s.Len() + streamNodeEntries - 1) / streamNodeEntries))
			c.W.WriteBulkString("radix-tree-nodes")
			c.W.WriteInt(int64((s.Len()+streamNodeEntries-1)/streamNodeEntries + 1))
			c.W.WriteBulkString("last-generated-id")
			c.W.WriteBulkString(s.LastID.String())
			c.W.WriteBulkString("max-deleted-entry-id")
			c.W.WriteBulkString(s.MaxDeletedID.String())
			c.W.WriteBulkString("entries-added")
			c.W.WriteInt(int64(s.EntriesAdded))
			c.W.WriteBulkString("recorded-first-entry-id")
			c.W.WriteBulkString(s.FirstID().String())
			c.W.WriteBulkString("groups")
			c.W.WriteInt(int64(len(s.Groups)))
			c.W.WriteBulkString("first-entry")
			if s.Len() > 0 {
				writeEntry(c.W, &s.Entries[0])
			} else {
				c.W.WriteNull()
			}
			c.W.WriteBulkString("last-entry")
			if s.Len() > 0 {
				writeEntry(c.W, &s.Entries[s.Len()-1])
			} else {
				c.W.WriteNull()
			}
			return
		}
		count := int64(10)
		if len(c.Args) == 6 && c.Is(4, "count") {
			n, ok := c.Int(5)
			if !ok {
				return
			}
			count = n
		}
		c.W.WriteMapLen(9)
		c.W.WriteBulkString("length")
		c.W.WriteInt(int64(s.Len()))
		c.W.WriteBulkString("radix-tree-keys")
		c.W.WriteInt(int64((s.Len() + streamNodeEntries - 1) / streamNodeEntries))
		c.W.WriteBulkString("radix-tree-nodes")
		c.W.WriteInt(int64((s.Len()+streamNodeEntries-1)/streamNodeEntries + 1))
		c.W.WriteBulkString("last-generated-id")
		c.W.WriteBulkString(s.LastID.String())
		c.W.WriteBulkString("max-deleted-entry-id")
		c.W.WriteBulkString(s.MaxDeletedID.String())
		c.W.WriteBulkString("entries-added")
		c.W.WriteInt(int64(s.EntriesAdded))
		c.W.WriteBulkString("recorded-first-entry-id")
		c.W.WriteBulkString(s.FirstID().String())
		c.W.WriteBulkString("entries")
		ents := streamRange(s, ds.StreamID{}, ds.MaxStreamID, nonZero(count), false)
		c.W.WriteArrayLen(len(ents))
		for i := range ents {
			writeEntry(c.W, &ents[i])
		}
		c.W.WriteBulkString("groups")
		names := s.GroupNames()
		c.W.WriteArrayLen(len(names))
		for _, n := range names {
			g := s.Groups[n]
			c.W.WriteMapLen(7)
			c.W.WriteBulkString("name")
			c.W.WriteBulkString(n)
			c.W.WriteBulkString("last-delivered-id")
			c.W.WriteBulkString(g.LastID.String())
			c.W.WriteBulkString("entries-read")
			writeEntriesRead(c.W, g.EntriesRead)
			c.W.WriteBulkString("lag")
			if lag, ok := s.Lag(g); ok {
				c.W.WriteInt(lag)
			} else {
				c.W.WriteNull()
			}
			c.W.WriteBulkString("pel-count")
			c.W.WriteInt(int64(len(g.PEL)))
			c.W.WriteBulkString("pending")
			var pes []*ds.PendingEntry
			g.PendingRange(ds.StreamID{}, ds.MaxStreamID, func(pe *ds.PendingEntry) bool {
				if count > 0 && int64(len(pes)) >= count {
					return false
				}
				pes = append(pes, pe)
				return true
			})
			c.W.WriteArrayLen(len(pes))
			for _, pe := range pes {
				c.W.WriteArrayLen(4)
				c.W.WriteBulkString(pe.ID.String())
				c.W.WriteBulkString(pe.Consumer.Name)
				c.W.WriteInt(pe.DeliveryTime)
				c.W.WriteInt(pe.DeliveryCount)
			}
			c.W.WriteBulkString("consumers")
			cn := g.ConsumerNames()
			c.W.WriteArrayLen(len(cn))
			for _, name := range cn {
				cons := g.Consumers[name]
				c.W.WriteMapLen(5)
				c.W.WriteBulkString("name")
				c.W.WriteBulkString(name)
				c.W.WriteBulkString("seen-time")
				c.W.WriteInt(cons.SeenTime)
				c.W.WriteBulkString("active-time")
				c.W.WriteInt(cons.ActiveTime)
				c.W.WriteBulkString("pel-count")
				c.W.WriteInt(int64(len(cons.PEL)))
				c.W.WriteBulkString("pending")
				var cp []*ds.PendingEntry
				g.PendingRange(ds.StreamID{}, ds.MaxStreamID, func(pe *ds.PendingEntry) bool {
					if pe.Consumer == cons {
						cp = append(cp, pe)
					}
					return count <= 0 || int64(len(cp)) < count
				})
				c.W.WriteArrayLen(len(cp))
				for _, pe := range cp {
					c.W.WriteArrayLen(3)
					c.W.WriteBulkString(pe.ID.String())
					c.W.WriteInt(pe.DeliveryTime)
					c.W.WriteInt(pe.DeliveryCount)
				}
			}
		}
	case "groups":
		if len(c.Args) != 3 {
			c.WrongSubArgs()
			return
		}
		names := s.GroupNames()
		c.W.WriteArrayLen(len(names))
		for _, n := range names {
			g := s.Groups[n]
			c.W.WriteMapLen(6)
			c.W.WriteBulkString("name")
			c.W.WriteBulkString(n)
			c.W.WriteBulkString("consumers")
			c.W.WriteInt(int64(len(g.Consumers)))
			c.W.WriteBulkString("pending")
			c.W.WriteInt(int64(len(g.PEL)))
			c.W.WriteBulkString("last-delivered-id")
			c.W.WriteBulkString(g.LastID.String())
			c.W.WriteBulkString("entries-read")
			writeEntriesRead(c.W, g.EntriesRead)
			c.W.WriteBulkString("lag")
			if lag, ok := s.Lag(g); ok {
				c.W.WriteInt(lag)
			} else {
				c.W.WriteNull()
			}
		}
	case "consumers":
		if len(c.Args) != 4 {
			c.WrongSubArgs()
			return
		}
		g := s.Groups[c.Arg(3)]
		if g == nil {
			c.Err("NOGROUP No such consumer group '" + c.Arg(3) + "' for key name '" + key + "'")
			return
		}
		cn := g.ConsumerNames()
		c.W.WriteArrayLen(len(cn))
		for _, name := range cn {
			cons := g.Consumers[name]
			c.W.WriteMapLen(4)
			c.W.WriteBulkString("name")
			c.W.WriteBulkString(name)
			c.W.WriteBulkString("pending")
			c.W.WriteInt(int64(len(cons.PEL)))
			c.W.WriteBulkString("idle")
			c.W.WriteInt(now - cons.SeenTime)
			c.W.WriteBulkString("inactive")
			if cons.ActiveTime >= 0 {
				c.W.WriteInt(now - cons.ActiveTime)
			} else {
				c.W.WriteInt(-1)
			}
		}
	default:
		c.UnknownSub()
	}
}

func writeEntriesRead(w *resp.Writer, n int64) {
	if n == ds.InvalidEntriesRead {
		w.WriteNull()
	} else {
		w.WriteInt(n)
	}
}
