package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/storage"
)

// Master/replica replication modelled on Redis PSYNC2:
//
//   - The master keeps a circular backlog of the propagation stream and a
//     replication ID + offset. Replicas connect, send REPLCONF and
//     PSYNC <replid> <offset>. If the requested offset is still in the
//     backlog the master answers +CONTINUE and streams the missing bytes,
//     otherwise +FULLRESYNC followed by a point-in-time snapshot.
//   - Snapshots are produced with copy-on-write, so a full sync never blocks
//     writers on the master.
//   - Replicas apply the stream through the normal dispatcher, forward it
//     verbatim to their own replicas (chained replication) and acknowledge
//     their offset every second (used by WAIT).

const replPingPeriod = 10 * time.Second

type backlog struct {
	buf     []byte
	idx     int   // next write position
	histlen int   // bytes held
	first   int64 // repl offset of first held byte (1 based like Redis)
}

func newBacklog(size int, masterOffset int64) *backlog {
	if size < 16*1024 {
		size = 16 * 1024
	}
	return &backlog{buf: make([]byte, size), first: masterOffset + 1}
}

func (b *backlog) write(p []byte) {
	for len(p) > 0 {
		n := copy(b.buf[b.idx:], p)
		b.idx = (b.idx + n) % len(b.buf)
		p = p[n:]
		b.histlen += n
		if b.histlen > len(b.buf) {
			b.first += int64(b.histlen - len(b.buf))
			b.histlen = len(b.buf)
		}
	}
}

// from returns bytes starting at repl offset off (must be in range).
func (b *backlog) from(off int64) []byte {
	skip := int(off - b.first)
	n := b.histlen - skip
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	start := (b.idx - b.histlen + skip + len(b.buf)*2) % len(b.buf)
	for i := 0; i < n; i++ {
		out[i] = b.buf[(start+i)%len(b.buf)]
	}
	return out
}

// replicaConn is the master-side view of a connected replica.
type replicaConn struct {
	c         *Client
	mu        sync.Mutex
	buf       []byte
	signal    chan struct{}
	ready     chan struct{}
	ackOffset atomic.Int64
	ackTime   atomic.Int64
	port      string
	state     atomic.Value // "wait_bgsave", "send_bulk", "online"
	closed    chan struct{}
	closeOnce sync.Once
}

const replicaOutputLimit = 256 << 20

func (r *replicaConn) enqueue(b []byte) {
	r.mu.Lock()
	r.buf = append(r.buf, b...)
	over := len(r.buf) > replicaOutputLimit
	r.mu.Unlock()
	if over {
		r.close()
		return
	}
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

func (r *replicaConn) close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		r.c.conn.Close()
	})
}

func (r *replicaConn) sender() {
	<-r.ready
	for {
		select {
		case <-r.closed:
			return
		case <-r.signal:
		}
		r.mu.Lock()
		data := r.buf
		r.buf = nil
		r.mu.Unlock()
		if len(data) == 0 {
			continue
		}
		r.c.wmu.Lock()
		_, err := r.c.conn.Write(data)
		r.c.wmu.Unlock()
		if err != nil {
			r.close()
			return
		}
	}
}

type replication struct {
	s *Server

	mu        sync.Mutex
	replID    string
	replID2   string
	secondOff int64

	// replica side
	isRep       atomic.Bool
	masterHost  string
	masterPort  string
	linkUp      atomic.Bool
	linkState   atomic.Value // string
	stopCh      chan struct{}
	masterOff   atomic.Int64
	lastIO      atomic.Int64
	syncing     atomic.Bool
	masterConn  net.Conn
	lastPingOut time.Time
}

func newReplication(s *Server) *replication {
	r := &replication{s: s, replID: randomHex(40), secondOff: -1}
	r.linkState.Store("none")
	return r
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha1Sum(b []byte) [20]byte { return sha1.Sum(b) }

func (r *replication) isReplica() bool { return r.isRep.Load() }

func (r *replication) changeReplID() {
	r.mu.Lock()
	r.replID = randomHex(40)
	r.mu.Unlock()
}

func (r *replication) backlogSize() int {
	p := r.s.prop
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backlog == nil {
		return 0
	}
	return len(p.backlog.buf)
}

func (r *replication) stop() {
	r.mu.Lock()
	if r.stopCh != nil {
		close(r.stopCh)
		r.stopCh = nil
	}
	if r.masterConn != nil {
		r.masterConn.Close()
	}
	r.mu.Unlock()
	p := r.s.prop
	p.mu.Lock()
	for rc := range p.replicas {
		rc.close()
	}
	p.mu.Unlock()
}

// cron runs once per second.
func (r *replication) cron() {
	p := r.s.prop
	p.mu.Lock()
	n := len(p.replicas)
	if n > 0 && time.Since(r.lastPingOut) >= replPingPeriod {
		r.lastPingOut = time.Now()
		ping := resp.AppendCommand(nil, []byte("PING"))
		p.offset += int64(len(ping))
		p.backlog.write(ping)
		for rc := range p.replicas {
			rc.enqueue(ping)
		}
	}
	p.mu.Unlock()
}

// ---- master side ----

func cmdReplconf(c *Ctx) {
	if len(c.Args)%2 != 1 {
		c.SyntaxErr()
		return
	}
	cl := c.cl
	for i := 1; i < len(c.Args); i += 2 {
		opt := strings.ToLower(c.Arg(i))
		val := c.Arg(i + 1)
		switch opt {
		case "listening-port":
			cl.replPort = val
		case "ip-address", "capa", "rdb-only", "rdb-filter-only", "rdb-channel":
		case "ack", "fack":
			if cl.replica != nil {
				n, _ := strconv.ParseInt(val, 10, 64)
				cl.replica.ackOffset.Store(n)
				cl.replica.ackTime.Store(time.Now().Unix())
			}
			return // no reply to ACK
		case "getack":
			if cl.isMaster {
				c.s.repl.sendAck()
			}
			return
		default:
			c.Err("ERR Unrecognized REPLCONF option: " + c.Arg(i))
			return
		}
	}
	c.W.WriteOK()
}

func cmdSync(c *Ctx) {
	cl := c.cl
	if cl.isMaster || c.inMulti || c.inScript {
		c.Err("ERR Replica can't interact with the keyspace")
		return
	}
	replid, off := "?", int64(-1)
	if strings.EqualFold(c.Cmd.Name, "psync") {
		replid = c.Arg(1)
		n, ok := parseInt(c.Args[2])
		if !ok {
			c.Err(errNotInt)
			return
		}
		off = n
	}
	// Flush pending replies first; from now on the connection carries the
	// replication stream.
	_ = cl.flush()
	rc := &replicaConn{c: cl, signal: make(chan struct{}, 1), ready: make(chan struct{}), closed: make(chan struct{}), port: cl.replPort}
	rc.state.Store("wait_bgsave")
	cl.replica = rc
	cl.isReplica = true
	c.s.repl.attachReplica(rc, replid, off)
}

func (r *replication) attachReplica(rc *replicaConn, replid string, off int64) {
	s := r.s
	p := s.prop
	go rc.sender()
	// Partial resync attempt.
	r.mu.Lock()
	id, id2, second := r.replID, r.replID2, r.secondOff
	r.mu.Unlock()
	p.mu.Lock()
	if p.backlog != nil && off > 0 && (replid == id || (replid == id2 && off <= second)) &&
		off >= p.backlog.first && off <= p.backlog.first+int64(p.backlog.histlen) {
		data := p.backlog.from(off)
		rc.c.wmu.Lock()
		_, err := rc.c.conn.Write([]byte("+CONTINUE " + id + "\r\n"))
		rc.c.wmu.Unlock()
		if err != nil {
			p.mu.Unlock()
			rc.close()
			return
		}
		rc.enqueue(data)
		p.replicas[rc] = struct{}{}
		p.mu.Unlock()
		rc.state.Store("online")
		close(rc.ready)
		s.stats.syncPartialOK.Add(1)
		s.log.Info("partial resynchronization accepted", "replica", rc.c.addr, "offset", off)
		return
	}
	p.mu.Unlock()
	if replid != "?" {
		s.stats.syncPartialErr.Add(1)
	}
	// Full resync with a copy-on-write snapshot.
	s.stats.syncFull.Add(1)
	var startOff int64
	var buf bytes.Buffer
	err := s.writeSnapshot(&buf, func() {
		p.mu.Lock()
		if p.backlog == nil {
			p.backlog = newBacklog(int(s.cfg.ReplBacklogSize.Load()), p.offset)
		}
		p.lastDB = -1
		startOff = p.offset
		p.replicas[rc] = struct{}{}
		p.mu.Unlock()
	})
	if err != nil {
		s.log.Warn("full sync snapshot failed", "err", err)
		r.removeReplica(rc)
		rc.close()
		return
	}
	rc.state.Store("send_bulk")
	header := fmt.Sprintf("+FULLRESYNC %s %d\r\n$%d\r\n", id, startOff, buf.Len())
	rc.c.wmu.Lock()
	_, err = rc.c.conn.Write([]byte(header))
	if err == nil {
		_, err = rc.c.conn.Write(buf.Bytes())
	}
	rc.c.wmu.Unlock()
	if err != nil {
		r.removeReplica(rc)
		rc.close()
		return
	}
	rc.state.Store("online")
	close(rc.ready)
	select {
	case rc.signal <- struct{}{}:
	default:
	}
	s.log.Info("synchronization with replica succeeded", "replica", rc.c.addr, "bytes", buf.Len())
}

func (r *replication) removeReplica(rc *replicaConn) {
	p := r.s.prop
	p.mu.Lock()
	delete(p.replicas, rc)
	p.mu.Unlock()
}

// serveReplica keeps reading REPLCONF ACKs from a replica connection.
func (r *replication) serveReplica(c *Client) {
	rc := c.replica
	defer func() {
		r.removeReplica(rc)
		rc.close()
	}()
	for {
		args, err := c.r.ReadCommand()
		if err != nil {
			return
		}
		if len(args) >= 3 && strings.EqualFold(string(args[0]), "replconf") && strings.EqualFold(string(args[1]), "ack") {
			n, _ := strconv.ParseInt(string(args[2]), 10, 64)
			rc.ackOffset.Store(n)
			rc.ackTime.Store(time.Now().Unix())
		}
	}
}

// waitReplicas implements WAIT.
func (r *replication) waitReplicas(n int, timeoutMs int64) int {
	p := r.s.prop
	p.mu.Lock()
	target := p.offset
	reps := make([]*replicaConn, 0, len(p.replicas))
	for rc := range p.replicas {
		reps = append(reps, rc)
	}
	if len(reps) > 0 && target > 0 {
		getack := resp.AppendCommandStrings(nil, "REPLCONF", "GETACK", "*")
		p.offset += int64(len(getack))
		p.backlog.write(getack)
		for _, rc := range reps {
			rc.enqueue(getack)
		}
	}
	p.mu.Unlock()
	count := func() int {
		k := 0
		for _, rc := range reps {
			if rc.ackOffset.Load() >= target {
				k++
			}
		}
		return k
	}
	var deadline time.Time
	if timeoutMs > 0 {
		deadline = time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	}
	for {
		k := count()
		if k >= n || len(reps) == 0 {
			return k
		}
		if timeoutMs > 0 && time.Now().After(deadline) {
			return k
		}
		select {
		case <-r.s.done:
			return k
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ---- replica side ----

func cmdReplicaOf(c *Ctx) {
	s := c.s
	if c.Is(1, "no") && c.Is(2, "one") {
		if s.repl.isReplica() {
			s.repl.promote()
		}
		c.W.WriteOK()
		return
	}
	if _, err := strconv.Atoi(c.Arg(2)); err != nil {
		c.Err("ERR Invalid master port")
		return
	}
	s.repl.startReplica(c.Arg(1), c.Arg(2))
	c.W.WriteSimple("OK")
}

func (r *replication) promote() {
	r.mu.Lock()
	if r.stopCh != nil {
		close(r.stopCh)
		r.stopCh = nil
	}
	if r.masterConn != nil {
		r.masterConn.Close()
	}
	r.replID2 = r.replID
	r.secondOff = r.s.prop.offsetValue() + 1
	r.replID = randomHex(40)
	r.mu.Unlock()
	r.isRep.Store(false)
	r.linkUp.Store(false)
	r.s.log.Info("MASTER MODE enabled")
}

func (p *propagator) offsetValue() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.offset
}

func (r *replication) startReplica(host, port string) {
	r.mu.Lock()
	if r.stopCh != nil {
		close(r.stopCh)
	}
	if r.masterConn != nil {
		r.masterConn.Close()
	}
	stop := make(chan struct{})
	r.stopCh = stop
	r.masterHost, r.masterPort = host, port
	r.mu.Unlock()
	r.isRep.Store(true)
	r.linkUp.Store(false)
	r.linkState.Store("connect")
	// Disconnect our own replicas: they must resync with the new history.
	p := r.s.prop
	p.mu.Lock()
	for rc := range p.replicas {
		rc.close()
	}
	p.mu.Unlock()
	r.s.log.Info("connecting to MASTER", "host", host, "port", port)
	go r.linkLoop(host, port, stop)
}

func (r *replication) linkLoop(host, port string, stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		err := r.syncWithMaster(host, port, stop)
		r.linkUp.Store(false)
		select {
		case <-stop:
			return
		default:
		}
		if err != nil {
			r.s.log.Warn("replication link error, retrying", "err", err)
		}
		r.linkState.Store("connect")
		select {
		case <-stop:
			return
		case <-time.After(time.Second):
		}
	}
}

func (r *replication) sendCmd(conn net.Conn, br *resp.Reader, args ...string) (string, error) {
	if _, err := conn.Write(resp.AppendCommandStrings(nil, args...)); err != nil {
		return "", err
	}
	line, err := br.ReadLine()
	if err != nil {
		return "", err
	}
	return line, nil
}

func (r *replication) syncWithMaster(host, port string, stop chan struct{}) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.masterConn = conn
	r.mu.Unlock()
	defer conn.Close()
	go func() {
		<-stop
		conn.Close()
	}()
	r.linkState.Store("handshake")
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	br := resp.NewReader(conn, 64*1024)
	br.MaxBulkLen = 1 << 62
	if pass := r.s.cfg.MasterAuth(); pass != "" {
		line, err := r.sendCmd(conn, br, "AUTH", pass)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(line, "+") {
			return errors.New("master AUTH failed: " + line)
		}
	}
	line, err := r.sendCmd(conn, br, "PING")
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "-NOAUTH") {
		return errors.New("master requires authentication (set masterauth)")
	}
	if _, err := r.sendCmd(conn, br, "REPLCONF", "listening-port", strconv.Itoa(r.s.cfg.Port)); err != nil {
		return err
	}
	if _, err := r.sendCmd(conn, br, "REPLCONF", "capa", "eof", "capa", "psync2"); err != nil {
		return err
	}
	r.mu.Lock()
	cachedID := r.replID
	r.mu.Unlock()
	cachedOff := r.s.prop.offsetValue()
	psyncID, psyncOff := "?", "-1"
	if r.masterOff.Load() > 0 || cachedOff > 0 {
		psyncID = cachedID
		psyncOff = strconv.FormatInt(cachedOff+1, 10)
	}
	line, err = r.sendCmd(conn, br, "PSYNC", psyncID, psyncOff)
	if err != nil {
		return err
	}
	r.syncing.Store(true)
	switch {
	case strings.HasPrefix(line, "+FULLRESYNC"):
		f := strings.Fields(line)
		if len(f) != 3 {
			return errors.New("bad FULLRESYNC reply: " + line)
		}
		off, _ := strconv.ParseInt(f[2], 10, 64)
		r.linkState.Store("sync")
		hdr, err := br.ReadLine()
		if err != nil {
			return err
		}
		if !strings.HasPrefix(hdr, "$") {
			return errors.New("bad bulk header: " + hdr)
		}
		n, err := strconv.ParseInt(hdr[1:], 10, 64)
		if err != nil {
			return err
		}
		payload, err := br.ReadRaw(n)
		if err != nil {
			return err
		}
		if err := r.loadFromMaster(payload); err != nil {
			return err
		}
		r.mu.Lock()
		r.replID = f[1]
		r.replID2 = ""
		r.secondOff = -1
		r.mu.Unlock()
		p := r.s.prop
		p.mu.Lock()
		p.offset = off
		p.lastDB = -1
		if p.backlog != nil {
			p.backlog = newBacklog(len(p.backlog.buf), off)
		}
		p.mu.Unlock()
		r.masterOff.Store(off)
		r.s.log.Info("MASTER <-> REPLICA sync: finished with success", "bytes", n)
	case strings.HasPrefix(line, "+CONTINUE"):
		f := strings.Fields(line)
		if len(f) == 2 && f[1] != cachedID {
			r.mu.Lock()
			r.replID2 = cachedID
			r.secondOff = cachedOff + 1
			r.replID = f[1]
			r.mu.Unlock()
		}
		r.s.log.Info("MASTER <-> REPLICA sync: partial resynchronization accepted")
	default:
		return errors.New("unexpected PSYNC reply: " + line)
	}
	r.syncing.Store(false)
	r.linkUp.Store(true)
	r.linkState.Store("connected")
	r.lastIO.Store(time.Now().Unix())
	_ = conn.SetDeadline(time.Time{})
	return r.streamFromMaster(conn, br, stop)
}

func (r *replication) loadFromMaster(payload []byte) error {
	s := r.s
	s.ks.HoldSnapshots()
	defer s.ks.ReleaseSnapshots()
	s.ks.LockAll(true)
	defer s.ks.UnlockAll(true)
	s.ks.FlushAll()
	_, err := s.loadSnapshot(bufio.NewReader(bytes.NewReader(payload)))
	s.watches.touchDB(-1)
	s.blocker.signalAll()
	return err
}

var ackMu sync.Mutex

func (r *replication) sendAck() {
	r.mu.Lock()
	conn := r.masterConn
	r.mu.Unlock()
	if conn == nil || !r.linkUp.Load() {
		return
	}
	off := r.s.prop.offsetValue()
	ackMu.Lock()
	_, _ = conn.Write(resp.AppendCommandStrings(nil, "REPLCONF", "ACK", strconv.FormatInt(off, 10)))
	ackMu.Unlock()
}

func (r *replication) streamFromMaster(conn net.Conn, br *resp.Reader, stop chan struct{}) error {
	s := r.s
	mc := &Client{s: s, conn: conn, isMaster: true, proto: 2, unblockCh: make(chan int, 1), created: time.Now(), authed: true}
	mc.w = resp.NewWriter(nil, 2)
	mc.r = br
	mc.name.Store("")
	mc.libName.Store("")
	mc.libVer.Store("")
	mc.lastCmd.Store("")
	mc.user = "default"
	mc.addr = conn.RemoteAddr().String()
	ackStop := make(chan struct{})
	defer close(ackStop)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ackStop:
				return
			case <-stop:
				return
			case <-t.C:
				r.sendAck()
			}
		}
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		args, err := br.ReadCommand()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("master closed the connection")
			}
			return err
		}
		if args == nil {
			continue
		}
		r.lastIO.Store(time.Now().Unix())
		raw := resp.AppendCommand(nil, args...)
		name := strings.ToLower(string(args[0]))
		selectDB := -1
		if name == "select" && len(args) == 2 {
			if n, ok := parseInt(args[1]); ok {
				selectDB = int(n)
			}
		}
		if name == "replconf" {
			// GETACK must account for its own bytes before replying.
			s.prop.feedRaw(raw, -1)
			if len(args) >= 2 && strings.EqualFold(string(args[1]), "getack") {
				r.sendAck()
			}
			continue
		}
		if name != "ping" {
			s.processCommand(mc, args)
			mc.w.Reset()
		}
		s.prop.feedRaw(raw, selectDB)
		r.masterOff.Store(s.prop.offsetValue())
	}
}

func (r *replication) writeRole(c *Ctx) {
	if r.isReplica() {
		state := r.linkState.Load().(string)
		port, _ := strconv.Atoi(r.masterPort)
		c.W.WriteArrayLen(5)
		c.W.WriteBulkString("slave")
		c.W.WriteBulkString(r.masterHost)
		c.W.WriteInt(int64(port))
		c.W.WriteBulkString(state)
		c.W.WriteInt(r.s.prop.offsetValue())
		return
	}
	p := r.s.prop
	p.mu.Lock()
	off := p.offset
	var reps []*replicaConn
	for rc := range p.replicas {
		reps = append(reps, rc)
	}
	p.mu.Unlock()
	c.W.WriteArrayLen(3)
	c.W.WriteBulkString("master")
	c.W.WriteInt(off)
	c.W.WriteArrayLen(len(reps))
	for _, rc := range reps {
		host, _, _ := net.SplitHostPort(rc.c.addr)
		c.W.WriteArrayLen(3)
		c.W.WriteBulkString(host)
		c.W.WriteBulkString(rc.port)
		c.W.WriteBulkString(strconv.FormatInt(rc.ackOffset.Load(), 10))
	}
}

func (r *replication) infoSection() string {
	var b strings.Builder
	b.WriteString("# Replication\r\n")
	p := r.s.prop
	p.mu.Lock()
	off := p.offset
	var reps []*replicaConn
	for rc := range p.replicas {
		reps = append(reps, rc)
	}
	var blFirst int64
	var blHist, blSize int
	if p.backlog != nil {
		blFirst, blHist, blSize = p.backlog.first, p.backlog.histlen, len(p.backlog.buf)
	}
	p.mu.Unlock()
	r.mu.Lock()
	id, id2, second := r.replID, r.replID2, r.secondOff
	r.mu.Unlock()
	if id2 == "" {
		id2 = strings.Repeat("0", 40)
	}
	if r.isReplica() {
		link := "down"
		if r.linkUp.Load() {
			link = "up"
		}
		fmt.Fprintf(&b, "role:slave\r\nmaster_host:%s\r\nmaster_port:%s\r\nmaster_link_status:%s\r\n", r.masterHost, r.masterPort, link)
		fmt.Fprintf(&b, "master_last_io_seconds_ago:%d\r\nmaster_sync_in_progress:%d\r\n", time.Now().Unix()-r.lastIO.Load(), b2i(r.syncing.Load()))
		fmt.Fprintf(&b, "slave_read_repl_offset:%d\r\nslave_repl_offset:%d\r\nslave_priority:100\r\nslave_read_only:%d\r\nreplica_announced:1\r\n",
			off, off, b2i(r.s.cfg.ReplicaReadOnly.Load()))
	} else {
		b.WriteString("role:master\r\n")
	}
	fmt.Fprintf(&b, "connected_slaves:%d\r\n", len(reps))
	for i, rc := range reps {
		host, _, _ := net.SplitHostPort(rc.c.addr)
		lag := time.Now().Unix() - rc.ackTime.Load()
		if rc.ackTime.Load() == 0 {
			lag = 0
		}
		fmt.Fprintf(&b, "slave%d:ip=%s,port=%s,state=%s,offset=%d,lag=%d\r\n", i, host, rc.port, rc.state.Load(), rc.ackOffset.Load(), lag)
	}
	fmt.Fprintf(&b, "master_failover_state:no-failover\r\nmaster_replid:%s\r\nmaster_replid2:%s\r\nmaster_repl_offset:%d\r\nsecond_repl_offset:%d\r\n",
		id, id2, off, second)
	fmt.Fprintf(&b, "repl_backlog_active:%d\r\nrepl_backlog_size:%d\r\nrepl_backlog_first_byte_offset:%d\r\nrepl_backlog_histlen:%d\r\n",
		b2i(blSize > 0), blSize, blFirst, blHist)
	return b.String()
}

var _ = storage.EntryOverhead
