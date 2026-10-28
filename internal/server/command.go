package server

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/storage"
)

// Flag describes command properties (mirrors Redis command flags).
type Flag uint64

const (
	FlagWrite Flag = 1 << iota
	FlagReadonly
	FlagDenyOOM
	FlagAdmin
	FlagPubSub
	FlagNoScript
	FlagBlocking
	FlagLoading
	FlagStale
	FlagSkipMonitor
	FlagSkipSlowlog
	FlagFast
	FlagNoAuth
	FlagMayReplicate
	FlagNoMulti
	FlagAllowBusy
	// FlagGlobal: the handler walks the whole keyspace and locks shards
	// itself when executed standalone (KEYS, SCAN, RANDOMKEY ...).
	FlagGlobal
	// FlagLockAll: the command must hold every shard lock (FLUSHALL, SWAPDB,
	// EVAL ...) and runs inside the snapshot barrier.
	FlagLockAll
	// FlagTxControl: MULTI/EXEC/DISCARD/WATCH/QUIT/RESET are never queued.
	FlagTxControl
	// FlagNoPropagate: never written to AOF/replicas even if dirty.
	FlagNoPropagate
)

var flagNames = map[string]Flag{
	"write": FlagWrite, "readonly": FlagReadonly, "denyoom": FlagDenyOOM, "admin": FlagAdmin,
	"pubsub": FlagPubSub, "noscript": FlagNoScript, "blocking": FlagBlocking, "loading": FlagLoading,
	"stale": FlagStale, "skip_monitor": FlagSkipMonitor, "skip_slowlog": FlagSkipSlowlog, "fast": FlagFast,
	"no_auth": FlagNoAuth, "may_replicate": FlagMayReplicate, "no_multi": FlagNoMulti,
	"allow_busy": FlagAllowBusy, "global": FlagGlobal, "lockall": FlagLockAll, "txctl": FlagTxControl,
	"noprop": FlagNoPropagate,
}

// Handler executes a command.
type Handler func(c *Ctx)

// KeysFunc extracts key argument positions. lockAll=true forces locking the
// entire keyspace (e.g. SORT with BY/GET patterns).
type KeysFunc func(args [][]byte) (pos []int, lockAll bool)

// Command is an entry in the command table.
type Command struct {
	Name     string
	Arity    int
	Flags    Flag
	First    int
	Last     int
	Step     int
	Group    string
	Keys     KeysFunc
	Handler  Handler
	Subs     []string
	Calls    atomic.Int64
	Usec     atomic.Int64
	Rejected atomic.Int64
	Failed   atomic.Int64
	// Notification metadata.
	NotifyClass int64
	Event       string
}

// Has reports whether f is set.
func (c *Command) Has(f Flag) bool { return c.Flags&f != 0 }

// KeyPositions returns the key argument indexes.
func (c *Command) KeyPositions(args [][]byte) ([]int, bool) {
	if c.Keys != nil {
		return c.Keys(args)
	}
	if c.First == 0 {
		return nil, false
	}
	last := c.Last
	if last < 0 {
		last = len(args) + last
	}
	var pos []int
	for i := c.First; i <= last && i < len(args); i += c.Step {
		pos = append(pos, i)
	}
	return pos, false
}

func parseFlags(s string) Flag {
	var f Flag
	for _, n := range strings.Fields(s) {
		v, ok := flagNames[n]
		if !ok {
			panic("unknown flag " + n)
		}
		f |= v
	}
	return f
}

// ---- execution context ----

// Ctx carries everything a handler needs.
type Ctx struct {
	s    *Server
	cl   *Client
	Cmd  *Command
	Args [][]byte
	W    *resp.Writer
	DB   *storage.DB

	unit  *execUnit
	dirty int64
	// prop replaces the propagated commands when set.
	prop     [][][]byte
	propSet  bool
	keys     []string
	allLocks bool // every shard is already locked (EXEC/script)
	inMulti  bool
	inScript bool
	isMaster bool // replication stream from our master

	// blocking support
	canBlock        bool
	blocked         bool
	blockKeys       []string
	blockTimeout    int64 // ms, 0 = forever
	timeoutReply    func(w *resp.Writer)
	blockState      any
	blockDeadlineMs int64
	errored         bool
}

// execUnit groups propagation for one atomic execution (command, EXEC,
// script) so it is written to AOF/replicas as a single block.
type execUnit struct {
	db     int
	cmds   []propCmd
	writes int
}

type propCmd struct {
	db   int
	args [][]byte
}

// Arg returns argument i as a string.
func (c *Ctx) Arg(i int) string { return string(c.Args[i]) }

// NArgs returns the number of arguments including the command name.
func (c *Ctx) NArgs() int { return len(c.Args) }

// Is reports a case-insensitive argument match.
func (c *Ctx) Is(i int, s string) bool { return strings.EqualFold(string(c.Args[i]), s) }

// Dirty marks the command as having modified the dataset n times.
func (c *Ctx) Dirty(n int64) { c.dirty += n }

// Rewrite replaces what is propagated for this command.
func (c *Ctx) Rewrite(args ...[]byte) {
	c.prop = append(c.prop, args)
	c.propSet = true
}

// RewriteStrings is Rewrite with string args.
func (c *Ctx) RewriteStrings(args ...string) {
	b := make([][]byte, len(args))
	for i, a := range args {
		b[i] = []byte(a)
	}
	c.Rewrite(b...)
}

// NoPropagate suppresses propagation of this command.
func (c *Ctx) NoPropagate() { c.propSet = true }

// Error replies.
const (
	errWrongType   = "WRONGTYPE Operation against a key holding the wrong kind of value"
	errNotInt      = "ERR value is not an integer or out of range"
	errNotFloat    = "ERR value is not a valid float"
	errSyntax      = "ERR syntax error"
	errNoSuchKey   = "ERR no such key"
	errOutOfRange  = "ERR index out of range"
	errOverflow    = "ERR increment or decrement would overflow"
	errNaN         = "ERR resulting score is not a number (NaN)"
	errMinMaxFloat = "ERR min or max is not a float"
	errLexRange    = "ERR min or max not valid string range item"
	errTimeout     = "ERR timeout is not a float or out of range"
	errNegTimeout  = "ERR timeout is negative"
	errDBIndex     = "ERR DB index is out of range"
	errBitOffset   = "ERR bit offset is not an integer or out of range"
	errBitValue    = "ERR bit is not an integer or out of range"
	errNoAuth      = "NOAUTH Authentication required."
	errOOM         = "OOM command not allowed when used memory > 'maxmemory'."
	errReadonly    = "READONLY You can't write against a read only replica."
	errCountNeg    = "ERR value is out of range, must be positive"
)

// Err writes an error reply.
func (c *Ctx) Err(msg string) {
	c.errored = true
	c.W.WriteError(msg)
}

// WrongType writes the WRONGTYPE error.
func (c *Ctx) WrongType() { c.Err(errWrongType) }

// SyntaxErr writes a syntax error.
func (c *Ctx) SyntaxErr() { c.Err(errSyntax) }

// WrongArgs replies with the wrong number of arguments error.
func (c *Ctx) WrongArgs() {
	c.Err("ERR wrong number of arguments for '" + strings.ToLower(c.Cmd.Name) + "' command")
}

// Int parses argument i as a 64-bit integer, replying with an error on
// failure.
func (c *Ctx) Int(i int) (int64, bool) {
	n, ok := parseInt(c.Args[i])
	if !ok {
		c.Err(errNotInt)
	}
	return n, ok
}

// IntRange parses an integer within [min, max].
func (c *Ctx) IntRange(i int, min, max int64, msg string) (int64, bool) {
	n, ok := parseInt(c.Args[i])
	if !ok {
		c.Err(errNotInt)
		return 0, false
	}
	if n < min || n > max {
		if msg == "" {
			msg = errNotInt
		}
		c.Err(msg)
		return 0, false
	}
	return n, true
}

// Float parses argument i as a float.
func (c *Ctx) Float(i int) (float64, bool) {
	f, ok := parseFloat(c.Args[i])
	if !ok {
		c.Err(errNotFloat)
	}
	return f, ok
}

// parseInt parses a strict base-10 int64 like Redis' string2ll.
func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 20 {
		return 0, false
	}
	if len(b) == 1 && b[0] == '0' {
		return 0, true
	}
	neg := b[0] == '-'
	i := 0
	if neg {
		i = 1
		if len(b) == 1 {
			return 0, false
		}
	}
	if b[i] < '1' || b[i] > '9' {
		return 0, false
	}
	var n uint64
	for ; i < len(b); i++ {
		d := b[i]
		if d < '0' || d > '9' {
			return 0, false
		}
		if n > (math.MaxUint64-uint64(d-'0'))/10 {
			return 0, false
		}
		n = n*10 + uint64(d-'0')
	}
	if neg {
		if n > 1<<63 {
			return 0, false
		}
		return -int64(n), true
	}
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

// parseFloat parses a float like Redis' string2ld (rejecting NaN, spaces).
func parseFloat(b []byte) (float64, bool) {
	if len(b) == 0 || len(b) > 5000 {
		return 0, false
	}
	if b[0] == ' ' || b[len(b)-1] == ' ' || bytes.ContainsAny(b, "\t\n\r\x00") {
		return 0, false
	}
	s := string(b)
	switch strings.ToLower(s) {
	case "inf", "+inf", "infinity", "+infinity":
		return math.Inf(1), true
	case "-inf", "-infinity":
		return math.Inf(-1), true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// Accept values that overflow? Redis rejects ERANGE.
		return 0, false
	}
	if math.IsNaN(f) {
		return 0, false
	}
	// Reject hex floats and underscores that Go accepts but C strtold does not.
	if strings.ContainsAny(s, "_xXpP") {
		return 0, false
	}
	return f, true
}

func formatInt(n int64) []byte { return strconv.AppendInt(nil, n, 10) }

// Lookup returns the live entry for key using the context DB.
func (c *Ctx) Lookup(key string) *storage.Entry {
	e := c.DB.Lookup(key)
	if c.Cmd.Has(FlagReadonly) && !c.inScript {
		if e == nil {
			c.s.stats.keyspaceMisses.Add(1)
		} else {
			c.s.stats.keyspaceHits.Add(1)
		}
	}
	return e
}
