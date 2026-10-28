package server

import (
	"sort"
	"strconv"
	"sync"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

type pubsubHub struct {
	mu       sync.RWMutex
	channels map[string]map[*Client]struct{}
	patterns map[string]map[*Client]struct{}
	shard    map[string]map[*Client]struct{}
}

func newPubsubHub() *pubsubHub {
	return &pubsubHub{
		channels: map[string]map[*Client]struct{}{},
		patterns: map[string]map[*Client]struct{}{},
		shard:    map[string]map[*Client]struct{}{},
	}
}

func hubAdd(m map[string]map[*Client]struct{}, name string, c *Client) {
	set := m[name]
	if set == nil {
		set = map[*Client]struct{}{}
		m[name] = set
	}
	set[c] = struct{}{}
}

func hubDel(m map[string]map[*Client]struct{}, name string, c *Client) {
	if set := m[name]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(m, name)
		}
	}
}

func (h *pubsubHub) unsubscribeAll(c *Client) {
	if c.subCount.Load() == 0 {
		return
	}
	h.mu.Lock()
	for ch := range c.subs {
		hubDel(h.channels, ch, c)
	}
	for p := range c.psubs {
		hubDel(h.patterns, p, c)
	}
	for ch := range c.ssubs {
		hubDel(h.shard, ch, c)
	}
	h.mu.Unlock()
	c.subs, c.psubs, c.ssubs = nil, nil, nil
	c.subCount.Store(0)
	c.nsubs.Store(0)
	c.npsubs.Store(0)
	c.nssubs.Store(0)
}

func encodePush(proto int, parts ...[]byte) []byte {
	w := resp.NewWriter(nil, proto)
	w.WritePushLen(len(parts))
	for _, p := range parts {
		w.WriteBulk(p)
	}
	return append([]byte(nil), w.Bytes()...)
}

var (
	bMessage  = []byte("message")
	bPMessage = []byte("pmessage")
	bSMessage = []byte("smessage")
)

// publish delivers msg and returns the number of receivers.
func (s *Server) publish(channel, msg []byte, sharded bool) int64 {
	h := s.pubsub
	h.mu.RLock()
	defer h.mu.RUnlock()
	var n int64
	var enc2, enc3 []byte
	deliver := func(c *Client, mk func(proto int) []byte, cache2, cache3 *[]byte) {
		if c.proto >= 3 {
			if *cache3 == nil {
				*cache3 = mk(3)
			}
			c.push(*cache3)
		} else {
			if *cache2 == nil {
				*cache2 = mk(2)
			}
			c.push(*cache2)
		}
		n++
	}
	if sharded {
		for c := range h.shard[string(channel)] {
			deliver(c, func(p int) []byte { return encodePush(p, bSMessage, channel, msg) }, &enc2, &enc3)
		}
		s.stats.pubsubMessages.Add(1)
		return n
	}
	for c := range h.channels[string(channel)] {
		deliver(c, func(p int) []byte { return encodePush(p, bMessage, channel, msg) }, &enc2, &enc3)
	}
	for pat, set := range h.patterns {
		if !ds.Match(pat, string(channel), false) {
			continue
		}
		var p2, p3 []byte
		pb := []byte(pat)
		for c := range set {
			deliver(c, func(p int) []byte { return encodePush(p, bPMessage, pb, channel, msg) }, &p2, &p3)
		}
	}
	s.stats.pubsubMessages.Add(1)
	return n
}

// notify emits keyspace notifications (notify-keyspace-events).
func (s *Server) notify(class int64, event, key string, db int) {
	flags := s.cfg.NotifyFlags.Load()
	if flags&class == 0 || flags&(config.NotifyKeyspace|config.NotifyKeyevent) == 0 {
		return
	}
	dbs := strconv.Itoa(db)
	if flags&config.NotifyKeyspace != 0 {
		s.publish([]byte("__keyspace@"+dbs+"__:"+key), []byte(event), false)
	}
	if flags&config.NotifyKeyevent != 0 {
		s.publish([]byte("__keyevent@"+dbs+"__:"+event), []byte(key), false)
	}
}

// ---- commands ----

type subKind int

const (
	kindChannel subKind = iota
	kindPattern
	kindShard
)

func (k subKind) names() (sub, unsub string) {
	switch k {
	case kindPattern:
		return "psubscribe", "punsubscribe"
	case kindShard:
		return "ssubscribe", "sunsubscribe"
	}
	return "subscribe", "unsubscribe"
}

func (c *Client) subMap(k subKind) *map[string]struct{} {
	switch k {
	case kindPattern:
		return &c.psubs
	case kindShard:
		return &c.ssubs
	}
	return &c.subs
}

func (h *pubsubHub) hubMap(k subKind) map[string]map[*Client]struct{} {
	switch k {
	case kindPattern:
		return h.patterns
	case kindShard:
		return h.shard
	}
	return h.channels
}

func (c *Client) updateSubCounts() {
	c.nsubs.Store(int32(len(c.subs)))
	c.npsubs.Store(int32(len(c.psubs)))
	c.nssubs.Store(int32(len(c.ssubs)))
	c.subCount.Store(int32(len(c.subs) + len(c.psubs) + len(c.ssubs)))
}

func (c *Ctx) subscribe(kind subKind) {
	cl := c.cl
	if cl == nil {
		c.Err("ERR This command is not allowed from script")
		return
	}
	cl.startPush()
	direct := c.W == cl.w
	if direct {
		cl.wmu.Lock()
		defer cl.wmu.Unlock()
	}
	subName, _ := kind.names()
	h := c.s.pubsub
	m := cl.subMap(kind)
	if *m == nil {
		*m = map[string]struct{}{}
	}
	for _, a := range c.Args[1:] {
		name := string(a)
		if _, ok := (*m)[name]; !ok {
			(*m)[name] = struct{}{}
			h.mu.Lock()
			hubAdd(h.hubMap(kind), name, cl)
			h.mu.Unlock()
		}
		cl.updateSubCounts()
		c.W.WritePushLen(3)
		c.W.WriteBulkString(subName)
		c.W.WriteBulk(a)
		if kind == kindShard {
			c.W.WriteInt(int64(len(*m)))
		} else {
			c.W.WriteInt(int64(cl.nsubs.Load() + cl.npsubs.Load()))
		}
	}
	if direct {
		_ = c.W.Flush()
	}
}

func (c *Ctx) unsubscribe(kind subKind) {
	cl := c.cl
	if cl == nil {
		c.Err("ERR This command is not allowed from script")
		return
	}
	_, unsubName := kind.names()
	h := c.s.pubsub
	m := cl.subMap(kind)
	var names []string
	if len(c.Args) > 1 {
		for _, a := range c.Args[1:] {
			names = append(names, string(a))
		}
	} else {
		for n := range *m {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	count := func() int64 {
		if kind == kindShard {
			return int64(len(*m))
		}
		return int64(len(cl.subs) + len(cl.psubs))
	}
	if len(names) == 0 {
		c.W.WritePushLen(3)
		c.W.WriteBulkString(unsubName)
		c.W.WriteNull()
		c.W.WriteInt(count())
		return
	}
	for _, n := range names {
		if _, ok := (*m)[n]; ok {
			delete(*m, n)
			h.mu.Lock()
			hubDel(h.hubMap(kind), n, cl)
			h.mu.Unlock()
		}
		cl.updateSubCounts()
		c.W.WritePushLen(3)
		c.W.WriteBulkString(unsubName)
		c.W.WriteBulkString(n)
		c.W.WriteInt(count())
	}
}

func cmdSubscribe(c *Ctx)    { c.subscribe(kindChannel) }
func cmdPSubscribe(c *Ctx)   { c.subscribe(kindPattern) }
func cmdSSubscribe(c *Ctx)   { c.subscribe(kindShard) }
func cmdUnsubscribe(c *Ctx)  { c.unsubscribe(kindChannel) }
func cmdPUnsubscribe(c *Ctx) { c.unsubscribe(kindPattern) }
func cmdSUnsubscribe(c *Ctx) { c.unsubscribe(kindShard) }

func cmdPublish(c *Ctx) {
	c.W.WriteInt(c.s.publish(c.Args[1], c.Args[2], false))
	c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
}

func cmdSPublish(c *Ctx) {
	c.W.WriteInt(c.s.publish(c.Args[1], c.Args[2], true))
	c.s.prop.propagate([]propCmd{{db: c.DB.Idx, args: c.Args}}, false)
}

func cmdPubsub(c *Ctx) {
	sub := string(lowerBytes(c.Args[1]))
	h := c.s.pubsub
	h.mu.RLock()
	defer h.mu.RUnlock()
	switch sub {
	case "channels", "shardchannels":
		if len(c.Args) > 3 {
			c.WrongSubArgs()
			return
		}
		src := h.channels
		if sub == "shardchannels" {
			src = h.shard
		}
		var out []string
		for ch := range src {
			if len(c.Args) == 3 && !ds.Match(c.Arg(2), ch, false) {
				continue
			}
			out = append(out, ch)
		}
		sort.Strings(out)
		c.W.WriteStrings(out)
	case "numsub", "shardnumsub":
		src := h.channels
		if sub == "shardnumsub" {
			src = h.shard
		}
		c.W.WriteMapLen(len(c.Args) - 2)
		for _, a := range c.Args[2:] {
			c.W.WriteBulk(a)
			c.W.WriteInt(int64(len(src[string(a)])))
		}
	case "numpat":
		if len(c.Args) != 2 {
			c.WrongSubArgs()
			return
		}
		c.W.WriteInt(int64(len(h.patterns)))
	case "help":
		c.writeHelp("PUBSUB", []string{
			"CHANNELS [<pattern>]", "    Return the currently active channels matching a <pattern> (default: '*').",
			"NUMPAT", "    Return number of subscriptions to patterns.",
			"NUMSUB [<channel> ...]", "    Return the number of subscribers for the specified channels, excluding",
			"    pattern subscriptions(default: no channels).",
			"SHARDCHANNELS [<pattern>]", "    Return the currently active shard level channels matching a <pattern> (default: '*').",
			"SHARDNUMSUB [<shardchannel> ...]", "    Return the number of subscribers for the specified shard level channel(s)",
		})
	default:
		c.UnknownSub()
	}
}
