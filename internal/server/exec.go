// exec.go runs commands in-process, without a TCP listener, for tests and
// microbenchmarks. A Session is a client connection whose socket is an
// in-memory buffer; commands go through the same processCommand path as
// network clients, including MULTI state, SELECT, pub/sub registration and
// authentication rejections.

package server

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
)

// ErrWouldBlock is returned by Exec for a blocking command whose timeout is
// zero (block forever), which an in-process caller could never wake.
var ErrWouldBlock = errors.New("exec: blocking command with zero timeout")

// syncBuf is a bytes.Buffer safe for the push goroutine (writer, under the
// client's wmu) and Session.Exec (reader) to use concurrently.
type syncBuf struct {
	mu sync.Mutex // guards b
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Read(p)
}

func (s *syncBuf) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

// Session is an in-process client connection. Apart from Close, which may be
// called from another goroutine to release a blocked Exec, it is not safe
// for concurrent use; create one Session per goroutine.
//
// Once a session has SUBSCRIBEd, push frames can arrive between calls, so an
// Exec result on a subscribed session may be a push frame rather than the
// command's reply. Use ReadPush to consume pushes deliberately.
type Session struct {
	s      *Server
	c      *Client
	buf    *syncBuf
	r      *resp.Reader
	closed atomic.Bool
}

// NewSession returns a fresh in-process client on database 0, authenticated
// as the default user and speaking RESP2. It is not listed in CLIENT LIST.
// Call Close when done so subscriptions and watches are released.
func (s *Server) NewSession() *Session {
	ss := &Session{s: s, buf: &syncBuf{}}
	c := &Client{
		s:         s,
		id:        s.nextID.Add(1),
		proto:     2,
		authed:    true,
		user:      "default",
		created:   time.Now(),
		unblockCh: make(chan int, 1),
	}
	c.writer = resp.NewWriter(ss.buf, 2)
	c.writer.Mu = &c.writeMu
	c.name.Store("")
	c.libName.Store("")
	c.libVer.Store("")
	c.lastCmd.Store("NULL")
	c.lastInteraction.Store(time.Now().Unix())
	c.multiLen.Store(-1)
	ss.c = c
	ss.r = resp.NewReader(ss.buf, 4096)
	ss.r.MaxBulkLen = resp.Unlimited
	ss.r.MaxMultiBulk = resp.Unlimited
	return ss
}

// Exec runs one command on a fresh Session and returns its reply. The
// session is closed before returning, so state such as SELECT, MULTI or
// SUBSCRIBE does not carry over.
func (s *Server) Exec(args ...string) (resp.Value, error) {
	ss := s.NewSession()
	defer ss.Close()
	return ss.Exec(args...)
}

// Exec runs one command and returns the parsed reply. A Redis error reply is
// returned as a value of type '-' with a nil error. It returns ErrWouldBlock
// for blocking commands with a zero timeout. Extra frames produced
// synchronously by the same command (e.g. SUBSCRIBE with several channels)
// are discarded after the first. On a subscribed session the returned value
// may be a push frame that arrived earlier; see Session.
func (ss *Session) Exec(args ...string) (resp.Value, error) {
	if len(args) == 0 {
		return resp.Value{}, errors.New("exec: empty command")
	}
	argv := make([][]byte, len(args))
	for i, a := range args {
		argv[i] = []byte(a)
	}
	if ss.c.multi == nil && blockingZeroTimeout(ss.s, argv) {
		return resp.Value{}, ErrWouldBlock
	}
	ss.s.processCommand(ss.c, argv)
	if err := ss.c.flush(); err != nil {
		return resp.Value{}, fmt.Errorf("exec: flush reply: %w", err)
	}
	v, err := ss.r.ReadValue()
	if err != nil {
		return resp.Value{}, fmt.Errorf("exec: read reply: %w", err)
	}
	for ss.r.Buffered() > 0 || ss.buf.len() > 0 {
		if _, err := ss.r.ReadValue(); err != nil {
			break
		}
	}
	return v, nil
}

// ReadPush returns the next frame the server sent to this session (typically
// a pub/sub message), waiting up to timeout for one to arrive.
func (ss *Session) ReadPush(timeout time.Duration) (resp.Value, error) {
	deadline := time.Now().Add(timeout)
	for ss.r.Buffered() == 0 && ss.buf.len() == 0 {
		if time.Now().After(deadline) {
			return resp.Value{}, errors.New("exec: no push before timeout")
		}
		time.Sleep(time.Millisecond)
	}
	v, err := ss.r.ReadValue()
	if err != nil {
		return resp.Value{}, fmt.Errorf("exec: read push: %w", err)
	}
	return v, nil
}

// Close releases the session's watches and pub/sub subscriptions, and wakes
// a blocked Exec. It is idempotent.
func (ss *Session) Close() {
	if !ss.closed.CompareAndSwap(false, true) {
		return
	}
	c, s := ss.c, ss.s
	s.pubsub.unsubscribeAll(c)
	s.watches.unwatchAll(c)
	s.removeMonitor(c)
	// Release an Exec blocked in waitBlocked (no socket, so no EOF signal).
	select {
	case c.unblockCh <- blockClosed:
	default:
	}
	c.pushMu.Lock()
	c.dead = true
	if c.pushCh != nil {
		close(c.pushCh)
	}
	c.pushMu.Unlock()
}

// timeoutRule locates the timeout (or BLOCK) value in a blocking command's
// arguments; args[0] is the command name. ok is false when absent.
type timeoutRule func(args [][]byte) (val []byte, ok bool)

// lastArg: BLPOP, BRPOP, BZPOPMIN, BZPOPMAX, BLMOVE, BRPOPLPUSH end with the timeout.
func lastArg(args [][]byte) ([]byte, bool) {
	if len(args) < 2 {
		return nil, false
	}
	return args[len(args)-1], true
}

// secondArg: BLMPOP and BZMPOP are "<cmd> timeout numkeys key... ".
func secondArg(args [][]byte) ([]byte, bool) {
	if len(args) < 2 {
		return nil, false
	}
	return args[1], true
}

// blockOption: XREAD and XREADGROUP take "BLOCK ms" before the STREAMS keyword.
func blockOption(args [][]byte) ([]byte, bool) {
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "STREAMS":
			return nil, false
		case "BLOCK":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return nil, false
		}
	}
	return nil, false
}

// blockingTimeoutRules maps each FlagBlocking command to where its timeout
// lives. A new blocking command must be added here or Exec could hang forever.
var blockingTimeoutRules = map[string]timeoutRule{
	"blpop":      lastArg,
	"brpop":      lastArg,
	"bzpopmin":   lastArg,
	"bzpopmax":   lastArg,
	"blmove":     lastArg,
	"brpoplpush": lastArg,
	"blmpop":     secondArg,
	"bzmpop":     secondArg,
	"xread":      blockOption,
	"xreadgroup": blockOption,
}

// blockingZeroTimeout reports whether args invoke a blocking command with a
// timeout of zero (wait forever). Unparsable timeouts are left to the
// handler to reject.
func blockingZeroTimeout(s *Server, args [][]byte) bool {
	cmd := s.lookupCommand(args[0])
	if cmd == nil || !cmd.Has(FlagBlocking) {
		return false
	}
	rule := blockingTimeoutRules[cmd.Name]
	if rule == nil {
		return false
	}
	val, ok := rule(args)
	if !ok {
		return false
	}
	f, err := strconv.ParseFloat(string(val), 64)
	return err == nil && f == 0
}
