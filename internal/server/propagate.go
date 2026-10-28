package server

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/storage"
)

// propagator turns executed write commands into a single ordered RESP
// stream that feeds the append-only file, the replication backlog and every
// connected replica. It is invoked while the writer still holds its shard
// locks, so commands touching the same key are always logged in execution
// order.
type propagator struct {
	s  *Server
	mu sync.Mutex

	lastDB int
	offset int64 // master_repl_offset

	backlog  *backlog
	replicas map[*replicaConn]struct{}

	aofFile   *os.File
	aofBuf    []byte
	aofSignal chan struct{}
	aofErr    error
	aofSize   int64
	lastFsync time.Time
	rewrite   *bytes.Buffer

	writerOnce sync.Once
	writerDone chan struct{}
}

func newPropagator(s *Server) *propagator {
	return &propagator{s: s, lastDB: -1, replicas: map[*replicaConn]struct{}{},
		aofSignal: make(chan struct{}, 1), writerDone: make(chan struct{})}
}

func (p *propagator) active() bool {
	return p.aofFile != nil || p.backlog != nil || p.rewrite != nil
}

var (
	bMulti = []byte("MULTI")
	bExec  = []byte("EXEC")
)

// propagate encodes and distributes commands. wrap forces MULTI/EXEC.
func (p *propagator) propagate(cmds []propCmd, wrap bool) {
	if len(cmds) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active() {
		return
	}
	var buf []byte
	if len(cmds) > 1 {
		wrap = true
	}
	if wrap {
		// The SELECT must precede MULTI.
		if cmds[0].db != p.lastDB {
			buf = resp.AppendCommand(buf, []byte("SELECT"), []byte(strconv.Itoa(cmds[0].db)))
			p.lastDB = cmds[0].db
		}
		buf = resp.AppendCommand(buf, bMulti)
	}
	for _, c := range cmds {
		if c.db != p.lastDB {
			buf = resp.AppendCommand(buf, []byte("SELECT"), []byte(strconv.Itoa(c.db)))
			p.lastDB = c.db
		}
		buf = resp.AppendCommand(buf, c.args...)
	}
	if wrap {
		buf = resp.AppendCommand(buf, bExec)
	}
	p.feedLocked(buf)
}

// feedRaw forwards bytes received from our master verbatim (replica side),
// tracking SELECTs so the database context stays consistent.
func (p *propagator) feedRaw(buf []byte, selectDB int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if selectDB >= 0 {
		p.lastDB = selectDB
	}
	if !p.active() {
		p.offset += int64(len(buf))
		return
	}
	p.feedLocked(buf)
}

func (p *propagator) feedLocked(buf []byte) {
	p.offset += int64(len(buf))
	if p.aofFile != nil {
		p.writeAOF(buf)
	}
	if p.rewrite != nil {
		p.rewrite.Write(buf)
	}
	if p.backlog != nil {
		p.backlog.write(buf)
		for r := range p.replicas {
			r.enqueue(buf)
		}
	}
}

// ---- AOF ----

func (p *propagator) aofPath() string {
	return filepath.Join(p.s.cfg.Dir, p.s.cfg.AppendFilename)
}

func (p *propagator) openAOF() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aofFile != nil {
		return nil
	}
	f, err := os.OpenFile(p.aofPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	if st != nil {
		p.aofSize = st.Size()
	}
	p.aofFile = f
	p.lastDB = -1
	p.writerOnce.Do(func() { go p.aofWriter() })
	return nil
}

func (p *propagator) writeAOF(buf []byte) {
	if p.s.cfg.AppendFsync == "always" {
		if len(p.aofBuf) > 0 {
			p.flushAOFLocked()
		}
		if _, err := p.aofFile.Write(buf); err != nil {
			p.aofErr = err
		}
		_ = p.aofFile.Sync()
		p.aofSize += int64(len(buf))
		return
	}
	p.aofBuf = append(p.aofBuf, buf...)
	select {
	case p.aofSignal <- struct{}{}:
	default:
	}
}

func (p *propagator) flushAOFLocked() {
	if p.aofFile == nil || len(p.aofBuf) == 0 {
		return
	}
	n, err := p.aofFile.Write(p.aofBuf)
	p.aofSize += int64(n)
	if err != nil {
		p.aofErr = err
		p.s.log.Warn("error writing to the AOF file", "err", err)
	}
	if cap(p.aofBuf) > 4<<20 {
		p.aofBuf = nil
	} else {
		p.aofBuf = p.aofBuf[:0]
	}
}

// aofWriter performs group commit: buffered commands are written as soon as
// possible (sub-millisecond) and fsynced every second for "everysec".
func (p *propagator) aofWriter() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.s.done:
			return
		case <-p.aofSignal:
			p.mu.Lock()
			p.flushAOFLocked()
			p.mu.Unlock()
		case <-t.C:
			p.mu.Lock()
			p.flushAOFLocked()
			f := p.aofFile
			fsync := p.s.cfg.AppendFsync == "everysec"
			p.mu.Unlock()
			if f != nil && fsync {
				_ = f.Sync()
			}
		}
	}
}

func (p *propagator) cron() {}

func (p *propagator) closeAOF() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aofFile != nil {
		p.flushAOFLocked()
		_ = p.aofFile.Sync()
		_ = p.aofFile.Close()
		p.aofFile = nil
	}
}

func (p *propagator) aofStats() (size int64, enabled bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aofSize, p.aofFile != nil, p.aofErr
}

// rewriteAOF builds a compact AOF: a point-in-time snapshot preamble
// followed by the commands executed while it was being written.
func (s *Server) rewriteAOF() error {
	if !s.aofRewr.CompareAndSwap(false, true) {
		return errors.New("Background append only file rewriting already in progress")
	}
	defer s.aofRewr.Store(false)
	p := s.prop
	dir := s.cfg.Dir
	tmp := filepath.Join(dir, fmt.Sprintf("temp-rewriteaof-%d.aof", time.Now().UnixNano()))
	f, err := os.Create(tmp)
	if err != nil {
		s.stats.lastAofRewriteOK.Store(false)
		return err
	}
	s.ks.BeginSnapshot(func() {
		p.mu.Lock()
		p.rewrite = &bytes.Buffer{}
		p.lastDB = -1
		p.mu.Unlock()
	})
	enc := storage.NewEncoder(f)
	for i := 0; i < s.ks.NumShards(); i++ {
		s.ks.SnapshotShard(i, func(db int, key string, e *storage.Entry) { enc.WriteEntry(db, key, e) })
	}
	s.ks.EndSnapshot()
	for _, code := range s.scripts.libraryCodes() {
		enc.WriteAux("function", []byte(code))
	}
	err = enc.Close()
	if err == nil {
		// Drain the rewrite buffer in steps to keep the final switch short.
		for {
			p.mu.Lock()
			if p.rewrite.Len() < 1<<20 {
				_, err = f.Write(p.rewrite.Bytes())
				if err == nil {
					err = f.Sync()
				}
				if err == nil {
					err = os.Rename(tmp, p.aofPath())
				}
				if err == nil {
					if p.aofFile != nil {
						p.flushAOFLocked()
						_ = p.aofFile.Close()
					}
					p.aofFile = f
					st, _ := f.Stat()
					if st != nil {
						p.aofSize = st.Size()
					}
					p.writerOnce.Do(func() { go p.aofWriter() })
				}
				p.rewrite = nil
				p.mu.Unlock()
				break
			}
			chunk := append([]byte(nil), p.rewrite.Bytes()...)
			p.rewrite.Reset()
			p.mu.Unlock()
			if _, err = f.Write(chunk); err != nil {
				p.mu.Lock()
				p.rewrite = nil
				p.mu.Unlock()
				break
			}
		}
	} else {
		p.mu.Lock()
		p.rewrite = nil
		p.mu.Unlock()
	}
	if err != nil {
		f.Close()
		os.Remove(tmp)
		s.stats.lastAofRewriteOK.Store(false)
		s.log.Warn("AOF rewrite failed", "err", err)
		return err
	}
	if !s.cfg.AppendOnly.Load() {
		// Rewrite requested while AOF is off: keep the file but stop logging.
		p.mu.Lock()
		if p.aofFile == f {
			p.aofFile = nil
			f.Close()
		}
		p.mu.Unlock()
	}
	s.stats.aofRewrites.Add(1)
	s.stats.lastAofRewriteOK.Store(true)
	s.log.Info("Background AOF rewrite finished successfully")
	return nil
}

// loadAOF replays the append-only file.
func (s *Server) loadAOF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	cr := &posReader{r: f}
	br := bufio.NewReaderSize(cr, 1<<16)
	peek, _ := br.Peek(6)
	keys := 0
	if string(peek) == "DCSNAP" {
		err := storage.DecodeWithAux(br, func(db int, key string, v any, exp int64) error {
			s.loadKey(db, key, v, exp)
			keys++
			return nil
		}, s.loadAux)
		if err != nil {
			return fmt.Errorf("bad AOF preamble: %w", err)
		}
	}
	fake := &Client{s: s, isMaster: true, proto: 2}
	fake.w = resp.NewWriter(nil, 2)
	fake.name.Store("aof-loader")
	fake.lastCmd.Store("")
	r := resp.NewReader(br, 1<<16)
	r.MaxBulkLen = 1 << 62
	cmds := 0
	var inMulti bool
	var lastGood int64 = cr.n - int64(br.Buffered())
	for {
		args, err := r.ReadCommand()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				s.log.Warn("AOF truncated, discarding incomplete tail", "valid_bytes", lastGood)
				_ = os.Truncate(path, lastGood)
				break
			}
			return fmt.Errorf("bad AOF format: %w", err)
		}
		lastGood = cr.n - int64(br.Buffered()) - int64(r.Buffered())
		if args == nil {
			continue
		}
		name := string(bytes.ToLower(args[0]))
		switch name {
		case "multi":
			inMulti = true
		case "exec":
			inMulti = false
		}
		s.processCommand(fake, args)
		fake.w.Reset()
		cmds++
	}
	if inMulti {
		// Unterminated transaction: discard it, like Redis does.
		fake.multi = nil
	}
	s.log.Info("DB loaded from append only file", "preamble_keys", keys, "commands", cmds)
	return nil
}

type posReader struct {
	r io.Reader
	n int64
}

func (p *posReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	return n, err
}
