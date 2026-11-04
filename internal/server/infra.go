package server

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
)

// ---- blocking operations (BLPOP, BZPOPMIN, XREAD BLOCK, ...) ----

type bkey struct {
	db  int
	key string
}

// waiter is one blocked command invocation. It stays registered (keeping
// its place in every key's queue) across retries until the command finishes.
type waiter struct {
	c    *Client
	seq  uint64 // registration order; immutable after register
	keys []bkey
	ch   chan struct{} // wake-up token, capacity 1
}

// wake delivers a wake-up token unless one is already pending.
func (w *waiter) wake() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

// blocker tracks clients blocked on keys and wakes them in FIFO order, like
// Redis's handleClientsBlockedOnKeys: a write wakes only the oldest waiter
// on each key, and every waiter that finishes or re-blocks passes the turn
// to the next one in the queue (see passOn), so waiters are served one at a
// time in the order they blocked and no wake-up is lost.
type blocker struct {
	mu      sync.Mutex
	seq     uint64             // guarded by mu
	waiters map[bkey][]*waiter // guarded by mu; each queue sorted by seq
	count   atomic.Int64       // registered waiters, read lock-free
}

func newBlocker() *blocker { return &blocker{waiters: map[bkey][]*waiter{}} }

// register queues a new waiter on keys. Callers hold the keys' shard locks
// so no write can slip in between the failed attempt and registration.
func (b *blocker) register(c *Client, db int, keys []string) *waiter {
	w := &waiter{c: c, ch: make(chan struct{}, 1)}
	b.mu.Lock()
	b.seq++
	w.seq = b.seq
	for _, k := range keys {
		bk := bkey{db, k}
		w.keys = append(w.keys, bk)
		b.waiters[bk] = append(b.waiters[bk], w)
	}
	b.mu.Unlock()
	b.count.Add(1)
	return w
}

// unregister removes w from its queues and passes the turn on, since w may
// have consumed (or been about to consume) a wake-up meant for its key.
func (b *blocker) unregister(w *waiter) {
	b.mu.Lock()
	for _, bk := range w.keys {
		q := slices.DeleteFunc(b.waiters[bk], func(o *waiter) bool { return o == w })
		if len(q) == 0 {
			delete(b.waiters, bk)
		} else {
			b.waiters[bk] = q
		}
	}
	b.passOnLocked(w)
	b.mu.Unlock()
	b.count.Add(-1)
}

// passOn wakes, on each of w's keys, the first waiter queued after w. A
// waiter calls it when it re-blocks after a wake-up, so data it could not
// use still reaches the waiters behind it.
func (b *blocker) passOn(w *waiter) {
	b.mu.Lock()
	b.passOnLocked(w)
	b.mu.Unlock()
}

func (b *blocker) passOnLocked(w *waiter) {
	for _, bk := range w.keys {
		q := b.waiters[bk]
		i, _ := slices.BinarySearchFunc(q, w.seq+1, func(o *waiter, seq uint64) int {
			return cmp.Compare(o.seq, seq)
		})
		if i < len(q) {
			q[i].wake()
		}
	}
}

// signal wakes the oldest client blocked on each of keys (called after
// writes, under locks).
func (b *blocker) signal(db int, keys []string) {
	if b.count.Load() == 0 {
		return
	}
	b.mu.Lock()
	for _, k := range keys {
		if q := b.waiters[bkey{db, k}]; len(q) > 0 {
			q[0].wake()
		}
	}
	b.mu.Unlock()
}

// signalAll wakes everyone (FLUSHALL, SWAPDB, replica load ...).
func (b *blocker) signalAll() {
	if b.count.Load() == 0 {
		return
	}
	b.mu.Lock()
	for _, q := range b.waiters {
		for _, w := range q {
			w.wake()
		}
	}
	b.mu.Unlock()
}

const (
	blockWoken = iota
	blockTimeout
	blockClosed
	blockUnblockTimeout
	blockUnblockErr
)

func (s *Server) waitBlocked(c *Client, w *waiter, ctx *Ctx) int {
	c.blockedNow.Store(true)
	defer c.blockedNow.Store(false)
	s.stats.totalBlocking.Add(1)
	var timer <-chan time.Time
	if ctx.blockTimeout > 0 {
		remaining := ctx.blockTimeout - ctx.s.Now().UnixMilli()
		if remaining <= 0 {
			return blockTimeout
		}
		// The deadline is measured on the injected clock, but the wait uses a
		// real timer: a fake clock does not drive timers, so tests that block
		// rely on real elapsed time (or an explicit wake-up) to finish.
		t := time.NewTimer(time.Duration(remaining) * time.Millisecond)
		defer t.Stop()
		timer = t.C
	}
	// Flush pending replies so pipelined clients see earlier results.
	_ = c.flush()
	closed := make(chan struct{})
	peekDone := make(chan struct{})
	go func() {
		// Detect client disconnects while blocked, so a disconnected client
		// never pops data it can no longer receive.
		defer close(peekDone)
		if c.conn == nil || c.r.Buffered() > 0 { // in-process sessions have no socket or reader
			return
		}
		_, err := c.r.Underlying().Peek(1)
		if err != nil && !isTimeoutErr(err) {
			close(closed)
		}
	}()
	defer func() {
		if c.conn != nil {
			_ = c.conn.SetReadDeadline(time.Now())
			<-peekDone
			_ = c.conn.SetReadDeadline(time.Time{})
		}
	}()
	select {
	case <-w.ch:
		return blockWoken
	case <-timer:
		return blockTimeout
	case how := <-c.unblockCh:
		return how
	case <-closed:
		return blockClosed
	case <-s.done:
		return blockClosed
	}
}

// Block is called by handlers that found no data. keys are the keys to wait
// on; timeoutMs is an absolute unix ms deadline (0 = forever).
func (c *Ctx) Block(keys []string, deadline int64, timeoutReply func(w *resp.Writer)) {
	c.blocked = true
	c.blockKeys = keys
	c.blockTimeout = deadline
	c.timeoutReply = timeoutReply
}

// parseTimeout parses a blocking timeout in seconds (float) into an
// absolute deadline in ms (0 = block forever).
func (c *Ctx) parseTimeout(i int, unitMs bool) (int64, bool) {
	var ms float64
	if unitMs {
		n, ok := parseInt(c.Args[i])
		if !ok {
			c.Err("ERR timeout is not an integer or out of range")
			return 0, false
		}
		ms = float64(n)
	} else {
		f, ok := parseFloat(c.Args[i])
		if !ok || f > 1e15 {
			c.Err(errTimeout)
			return 0, false
		}
		ms = f * 1000
	}
	if ms < 0 {
		c.Err(errNegTimeout)
		return 0, false
	}
	if ms == 0 {
		return 0, true
	}
	return c.NowMs() + int64(ms+0.5), true
}

// ---- WATCH registry ----

type watchKey struct {
	db  int
	key string
}

type watchRegistry struct {
	mu    sync.Mutex
	m     map[watchKey]map[*Client]struct{}
	count atomic.Int64
}

func newWatchRegistry() *watchRegistry {
	return &watchRegistry{m: map[watchKey]map[*Client]struct{}{}}
}

func (r *watchRegistry) watch(c *Client, db int, key string) {
	wk := watchKey{db, key}
	for _, k := range c.watched {
		if k == wk {
			return
		}
	}
	c.watched = append(c.watched, wk)
	c.watchCount.Store(int32(len(c.watched)))
	r.mu.Lock()
	set := r.m[wk]
	if set == nil {
		set = map[*Client]struct{}{}
		r.m[wk] = set
	}
	set[c] = struct{}{}
	r.mu.Unlock()
	r.count.Add(1)
}

func (r *watchRegistry) unwatchAll(c *Client) {
	if len(c.watched) == 0 {
		c.casDirty.Store(false)
		return
	}
	r.mu.Lock()
	for _, wk := range c.watched {
		if set := r.m[wk]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(r.m, wk)
			}
		}
	}
	r.mu.Unlock()
	r.count.Add(-int64(len(c.watched)))
	c.watched = nil
	c.watchCount.Store(0)
	c.casDirty.Store(false)
}

func (r *watchRegistry) touch(db int, keys []string) {
	if r.count.Load() == 0 {
		return
	}
	r.mu.Lock()
	for _, k := range keys {
		for c := range r.m[watchKey{db, k}] {
			c.casDirty.Store(true)
		}
	}
	r.mu.Unlock()
}

// touchDB invalidates every watcher of db (-1 = all dbs).
func (r *watchRegistry) touchDB(db int) {
	if r.count.Load() == 0 {
		return
	}
	r.mu.Lock()
	for wk, set := range r.m {
		if db == -1 || wk.db == db {
			for c := range set {
				c.casDirty.Store(true)
			}
		}
	}
	r.mu.Unlock()
}

// ---- SLOWLOG ----

type slowEntry struct {
	id       int64
	ts       int64
	duration int64
	args     [][]byte
	addr     string
	name     string
}

type slowlog struct {
	mu      sync.Mutex
	entries []slowEntry
	nextID  int64
}

func newSlowlog() *slowlog { return &slowlog{} }

func (l *slowlog) maybeAdd(s *Server, c *Client, args [][]byte, d time.Duration) {
	thr := s.cfg.SlowlogSlowerThan.Load()
	if thr < 0 || d.Microseconds() < thr {
		return
	}
	max := s.cfg.SlowlogMaxLen.Load()
	n := len(args)
	if n > 32 {
		n = 32
	}
	cp := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		a := args[i]
		if i == 31 && len(args) > 32 {
			cp = append(cp, []byte("... ("+strconv.Itoa(len(args)-31)+" more arguments)"))
			break
		}
		if len(a) > 128 {
			a = append(append([]byte(nil), a[:128]...), []byte("... ("+strconv.Itoa(len(a)-128)+" more bytes)")...)
		} else {
			a = append([]byte(nil), a...)
		}
		cp = append(cp, a)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := slowEntry{id: l.nextID, ts: time.Now().Unix(), duration: d.Microseconds(), args: cp,
		addr: c.addr, name: c.name.Load().(string)}
	l.nextID++
	l.entries = append([]slowEntry{e}, l.entries...)
	if int64(len(l.entries)) > max {
		l.entries = l.entries[:max]
	}
}

func cmdSlowlog(c *Ctx) {
	sub := strings.ToLower(c.Arg(1))
	l := c.s.slowlog
	switch sub {
	case "get":
		if len(c.Args) > 3 {
			c.WrongSubArgs()
			return
		}
		count := int64(10)
		if len(c.Args) == 3 {
			n, ok := parseInt(c.Args[2])
			if !ok || n < -1 {
				c.Err("ERR count should be greater than or equal to -1")
				return
			}
			count = n
		}
		l.mu.Lock()
		ents := l.entries
		if count >= 0 && int64(len(ents)) > count {
			ents = ents[:count]
		}
		c.W.WriteArrayLen(len(ents))
		for _, e := range ents {
			c.W.WriteArrayLen(6)
			c.W.WriteInt(e.id)
			c.W.WriteInt(e.ts)
			c.W.WriteInt(e.duration)
			c.W.WriteBulks(e.args)
			c.W.WriteBulkString(e.addr)
			c.W.WriteBulkString(e.name)
		}
		l.mu.Unlock()
	case "len":
		l.mu.Lock()
		c.W.WriteInt(int64(len(l.entries)))
		l.mu.Unlock()
	case "reset":
		l.mu.Lock()
		l.entries = nil
		l.mu.Unlock()
		c.W.WriteOK()
	case "help":
		c.writeHelp("SLOWLOG", []string{
			"GET [<count>]", "    Return top <count> entries from the slowlog (default: 10, -1 mean all).",
			"    Entries are made of:", "    id, timestamp, time in microseconds, arguments array, client IP and port,",
			"    client name", "LEN", "    Return the length of the slowlog.", "RESET", "    Reset the slowlog.",
		})
	default:
		c.UnknownSub()
	}
}

// ---- generic sub-command helpers ----

// UnknownSub replies with the unknown subcommand error.
func (c *Ctx) UnknownSub() {
	c.Err("ERR unknown subcommand '" + truncate(c.Arg(1), 128) + "'. Try " + strings.ToUpper(c.Cmd.Name) + " HELP.")
}

// WrongSubArgs replies with the wrong number of arguments error for a
// container command's subcommand.
func (c *Ctx) WrongSubArgs() {
	c.Err("ERR wrong number of arguments for '" + c.Cmd.Name + "|" + strings.ToLower(c.Arg(1)) + "' command")
}

func (c *Ctx) writeHelp(name string, lines []string) {
	all := append([]string{name + " <subcommand> [<arg> [value] [opt] ...]. Subcommands are:"}, lines...)
	all = append(all, "HELP", "    Print this help.")
	c.W.WriteArrayLen(len(all))
	for _, l := range all {
		c.W.WriteSimple(l)
	}
}

func lowerBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			ch += 32
		}
		out[i] = ch
	}
	return out
}
