package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
)

const (
	replyOn = iota
	replyOff
	replySkip
	replySkipNext
)

// Client is one connection.
type Client struct {
	s     *Server
	id    int64
	conn  net.Conn
	r     *resp.Reader
	w     *resp.Writer
	wmu   sync.Mutex
	db    int
	proto int

	name    atomic.Value // string
	libName atomic.Value
	libVer  atomic.Value
	authed  bool
	user    string

	multi    *multiState
	watched  []watchKey
	casDirty atomic.Bool

	subs, psubs, ssubs    map[string]struct{}
	subCount              atomic.Int32
	nsubs, npsubs, nssubs atomic.Int32
	multiLen              atomic.Int32
	watchCount            atomic.Int32
	replPort              string

	pushMu sync.Mutex
	pushCh chan []byte
	dead   bool

	isMaster  bool
	isReplica bool
	isMonitor bool
	replica   *replicaConn

	created         time.Time
	lastInteraction atomic.Int64
	lastCmd         atomic.Value
	blockedNow      atomic.Bool
	unblockCh       chan int
	replyMode       int
	noEvict         bool
	noTouch         bool
	addr, laddr     string
	closeAfterReply bool
	dbAtomic        atomic.Int32
}

func (s *Server) newClient(conn net.Conn) *Client {
	c := &Client{
		s:         s,
		id:        s.nextID.Add(1),
		conn:      conn,
		r:         resp.NewReader(&countingReader{conn, &s.stats.netInput}, 16*1024),
		proto:     2,
		created:   time.Now(),
		unblockCh: make(chan int, 1),
		addr:      conn.RemoteAddr().String(),
		laddr:     conn.LocalAddr().String(),
	}
	c.w = resp.NewWriter(&countingWriter{conn, &s.stats.netOutput}, 2)
	c.r.MaxBulkLen = s.cfg.ProtoMaxBulkLen.Load()
	c.name.Store("")
	c.libName.Store("")
	c.libVer.Store("")
	c.lastCmd.Store("NULL")
	c.lastInteraction.Store(time.Now().Unix())
	c.multiLen.Store(-1)
	c.authed = !s.cfg.RequirePassSet.Load()
	c.user = "default"
	s.clientsMu.Lock()
	s.clients[c.id] = c
	s.clientsMu.Unlock()
	return c
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// flush sends buffered replies.
func (c *Client) flush() error {
	c.wmu.Lock()
	err := c.w.Flush()
	c.wmu.Unlock()
	return err
}

func (c *Client) inPubSub() bool { return c.subCount.Load() > 0 }

func (s *Server) serveClient(c *Client) {
	defer s.closeClient(c)
	for {
		if to := s.cfg.Timeout.Load(); to > 0 && !c.inPubSub() && !c.isMaster && !c.isReplica {
			_ = c.conn.SetReadDeadline(time.Now().Add(time.Duration(to) * time.Second))
		} else if to > 0 {
			_ = c.conn.SetReadDeadline(time.Time{})
		}
		args, err := c.r.ReadCommand()
		if err != nil {
			var pe *resp.ProtocolError
			if errors.As(err, &pe) {
				c.w.WriteError("ERR " + pe.Error())
				_ = c.flush()
			}
			return
		}
		if args == nil {
			continue
		}
		c.lastInteraction.Store(time.Now().Unix())
		s.processCommand(c, args)
		if c.closeAfterReply {
			_ = c.flush()
			return
		}
		if c.r.Buffered() == 0 || c.w.Len() > 64*1024 {
			if err := c.flush(); err != nil {
				return
			}
		}
		if c.isReplica {
			// Connection handed over to replication streaming.
			s.repl.serveReplica(c)
			return
		}
	}
}

func (s *Server) closeClient(c *Client) {
	c.conn.Close()
	s.pubsub.unsubscribeAll(c)
	s.watches.unwatchAll(c)
	s.removeMonitor(c)
	c.pushMu.Lock()
	c.dead = true
	if c.pushCh != nil {
		close(c.pushCh)
	}
	c.pushMu.Unlock()
	s.clientsMu.Lock()
	delete(s.clients, c.id)
	s.clientsMu.Unlock()
	if c.replica != nil {
		s.repl.removeReplica(c.replica)
	}
}

// startPush lazily starts the goroutine delivering out-of-band messages
// (pub/sub, MONITOR) so slow subscribers never block publishers.
func (c *Client) startPush() {
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	if c.pushCh != nil || c.dead {
		return
	}
	limit := c.s.cfg.PubsubBufferLimit.Load()
	if limit < 16 {
		limit = 16
	}
	if limit > 1<<20 {
		limit = 1 << 20
	}
	ch := make(chan []byte, limit)
	c.pushCh = ch
	go func() {
		for msg := range ch {
			c.wmu.Lock()
			_, err := c.w.Out.Write(msg)
			c.wmu.Unlock()
			if err != nil {
				c.conn.Close()
				for range ch {
				}
				return
			}
		}
	}()
}

// push queues an encoded message; a full queue disconnects the client
// (equivalent to Redis' client-output-buffer-limit for pubsub).
func (c *Client) push(msg []byte) {
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	if c.dead || c.pushCh == nil {
		return
	}
	select {
	case c.pushCh <- msg:
	default:
		c.dead = true
		close(c.pushCh)
		c.s.stats.evictedClients.Add(1)
		c.s.log.Warn("client closed for overcoming of output buffer limits", "id", c.id, "addr", c.addr)
		c.conn.Close()
	}
}

// flags renders CLIENT LIST flags.
func (c *Client) flagString() string {
	var b strings.Builder
	if c.isMaster {
		b.WriteByte('M')
	}
	if c.isReplica {
		b.WriteByte('S')
	}
	if c.isMonitor {
		b.WriteByte('O')
	}
	if c.inPubSub() {
		b.WriteByte('P')
	}
	if c.multiLen.Load() >= 0 {
		b.WriteByte('x')
	}
	if c.blockedNow.Load() {
		b.WriteByte('b')
	}
	if c.casDirty.Load() {
		b.WriteByte('d')
	}
	if c.noEvict {
		b.WriteByte('e')
	}
	if c.noTouch {
		b.WriteByte('T')
	}
	if b.Len() == 0 {
		return "N"
	}
	return b.String()
}

func (c *Client) info() string {
	now := time.Now()
	multi := int(c.multiLen.Load())
	return fmt.Sprintf("id=%d addr=%s laddr=%s fd=8 name=%s age=%d idle=%d flags=%s db=%d sub=%d psub=%d ssub=%d multi=%d qbuf=0 qbuf-free=0 argv-mem=0 multi-mem=0 tot-mem=0 rbs=16384 rbp=0 obl=0 oll=0 omem=0 events=r cmd=%s user=%s redir=-1 resp=%d lib-name=%s lib-ver=%s",
		c.id, c.addr, c.laddr, c.name.Load().(string), int64(now.Sub(c.created).Seconds()),
		now.Unix()-c.lastInteraction.Load(), c.flagString(), c.dbAtomic.Load(), c.nsubs.Load(), c.npsubs.Load(), c.nssubs.Load(),
		multi, c.lastCmd.Load().(string), c.user, c.proto, c.libName.Load().(string), c.libVer.Load().(string))
}
