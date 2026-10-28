// Package server implements the DistCache network server: connection
// handling, the command dispatcher and every Redis command family.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/storage"
	"github.com/rabbicse/distcache/pkg/logger"
)

// Version is the DistCache version. RedisVersion is the Redis version whose
// command set and replies DistCache implements (reported to clients).
const (
	Version      = "1.0.0"
	RedisVersion = "7.4.0"
)

type stats struct {
	commands          atomic.Int64
	connections       atomic.Int64
	rejected          atomic.Int64
	expiredKeys       atomic.Int64
	evictedKeys       atomic.Int64
	keyspaceHits      atomic.Int64
	keyspaceMisses    atomic.Int64
	netInput          atomic.Int64
	netOutput         atomic.Int64
	pubsubMessages    atomic.Int64
	errorReplies      atomic.Int64
	totalBlocking     atomic.Int64
	opsSamples        [16]int64
	opsSampleIdx      int
	lastOpsCount      int64
	instantaneousOps  atomic.Int64
	expiredStalePerc  atomic.Int64
	totalReadsProc    atomic.Int64
	totalWritesProc   atomic.Int64
	syncFull          atomic.Int64
	syncPartialOK     atomic.Int64
	syncPartialErr    atomic.Int64
	evictedClients    atomic.Int64
	unexpectedErrors  atomic.Int64
	dirtySinceSave    atomic.Int64
	lastSaveTime      atomic.Int64
	lastBgsaveOK      atomic.Bool
	lastBgsaveSeconds atomic.Int64
	aofRewrites       atomic.Int64
	lastAofRewriteOK  atomic.Bool
}

// Server is a DistCache instance.
type Server struct {
	cfg  *config.Config
	ks   *storage.Keyspace
	log  *slog.Logger
	cmds map[string]*Command

	lnMu      sync.Mutex
	listeners []net.Listener

	clientsMu sync.RWMutex
	clients   map[int64]*Client
	nextID    atomic.Int64

	stats   stats
	pubsub  *pubsubHub
	blocker *blocker
	watches *watchRegistry
	prop    *propagator
	repl    *replication
	scripts *scriptEngine
	slowlog *slowlog

	monitorsMu sync.RWMutex
	monitors   map[*Client]struct{}
	monitorN   atomic.Int32

	runID     string
	startTime time.Time
	loading   atomic.Bool
	bgsave    atomic.Bool
	aofRewr   atomic.Bool
	closing   atomic.Bool
	pauseMu   sync.RWMutex
	pauseEnd  atomic.Int64
	pauseAll  atomic.Bool

	done     chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup
	ready    chan struct{}
	addr     atomic.Value // string
}

// New creates a server from a configuration.
func New(cfg *config.Config, log *slog.Logger) *Server {
	if log == nil {
		log = logger.Discard()
	}
	ks := storage.New(cfg.Shards, cfg.Databases)
	ks.LFULogFactor.Store(cfg.LFULogFactor.Load())
	ks.LFUDecayTime.Store(cfg.LFUDecayTime.Load())
	var id [20]byte
	_, _ = rand.Read(id[:])
	s := &Server{
		cfg:       cfg,
		ks:        ks,
		log:       log,
		clients:   make(map[int64]*Client),
		monitors:  make(map[*Client]struct{}),
		runID:     hex.EncodeToString(id[:]),
		startTime: time.Now(),
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
	}
	s.stats.lastSaveTime.Store(time.Now().Unix())
	s.stats.lastBgsaveOK.Store(true)
	s.stats.lastAofRewriteOK.Store(true)
	s.pubsub = newPubsubHub()
	s.blocker = newBlocker()
	s.watches = newWatchRegistry()
	s.slowlog = newSlowlog()
	s.prop = newPropagator(s)
	s.repl = newReplication(s)
	s.scripts = newScriptEngine(s)
	s.registerCommands()
	return s
}

// Config returns the live configuration.
func (s *Server) Config() *config.Config { return s.cfg }

// Keyspace exposes the dataset (tests, embedding).
func (s *Server) Keyspace() *storage.Keyspace { return s.ks }

// Addr returns the first listening address once Start has bound it.
func (s *Server) Addr() string {
	if v, ok := s.addr.Load().(string); ok {
		return v
	}
	return ""
}

// Ready is closed once the server accepts connections.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Start loads persisted data, binds listeners and serves until Shutdown.
func (s *Server) Start() error {
	if err := s.loadData(); err != nil {
		return err
	}
	if s.cfg.AppendOnly.Load() {
		if err := s.prop.openAOF(); err != nil {
			return fmt.Errorf("open AOF: %w", err)
		}
	}
	var lns []net.Listener
	for _, b := range s.cfg.Bind {
		if b == "*" {
			b = "0.0.0.0"
		}
		if b == "-::*" || b == "::*" {
			b = "::"
		}
		addr := net.JoinHostPort(b, strconv.Itoa(s.cfg.Port))
		lc := net.ListenConfig{KeepAlive: time.Duration(s.cfg.TCPKeepAlive.Load()) * time.Second}
		ln, err := lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			return err
		}
		lns = append(lns, ln)
	}
	if s.cfg.UnixSocket != "" {
		_ = os.Remove(s.cfg.UnixSocket)
		ln, err := net.Listen("unix", s.cfg.UnixSocket)
		if err != nil {
			return err
		}
		lns = append(lns, ln)
	}
	if len(lns) == 0 {
		return errors.New("no listeners configured")
	}
	s.lnMu.Lock()
	s.listeners = lns
	s.lnMu.Unlock()
	s.addr.Store(lns[0].Addr().String())
	if s.cfg.PidFile != "" {
		_ = os.WriteFile(s.cfg.PidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	}
	s.log.Info("DistCache ready to accept connections", "version", Version, "addr", s.Addr(),
		"shards", s.ks.NumShards(), "databases", s.ks.NumDB)
	s.wg.Add(1)
	go s.cron()
	if ro := s.cfg.ReplicaOf; ro != "" {
		var host, port string
		if _, err := fmt.Sscan(ro, &host, &port); err == nil {
			s.repl.startReplica(host, port)
		}
	}
	close(s.ready)
	errCh := make(chan error, len(lns))
	for _, ln := range lns {
		ln := ln
		go func() { errCh <- s.acceptLoop(ln) }()
	}
	var firstErr error
	for range lns {
		if err := <-errCh; err != nil && firstErr == nil && !s.closing.Load() {
			firstErr = err
		}
	}
	s.wg.Wait()
	return firstErr
}

func (s *Server) acceptLoop(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.closing.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		s.stats.connections.Add(1)
		s.clientsMu.RLock()
		n := int64(len(s.clients))
		s.clientsMu.RUnlock()
		if n >= s.cfg.MaxClients.Load() {
			s.stats.rejected.Add(1)
			_, _ = conn.Write([]byte("-ERR max number of clients reached\r\n"))
			conn.Close()
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		cl := s.newClient(conn)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveClient(cl)
		}()
	}
}

// Shutdown stops accepting connections, optionally saves, and closes all
// clients.
func (s *Server) Shutdown(save bool) error {
	var err error
	s.doneOnce.Do(func() {
		s.closing.Store(true)
		s.lnMu.Lock()
		for _, ln := range s.listeners {
			ln.Close()
		}
		s.lnMu.Unlock()
		s.repl.stop()
		if save {
			if e := s.saveSync(); e != nil {
				err = e
				s.log.Warn("error saving on shutdown", "err", e)
			}
		}
		s.prop.closeAOF()
		close(s.done)
		s.clientsMu.RLock()
		for _, c := range s.clients {
			c.conn.Close()
		}
		s.clientsMu.RUnlock()
		if s.cfg.UnixSocket != "" {
			_ = os.Remove(s.cfg.UnixSocket)
		}
		if s.cfg.PidFile != "" {
			_ = os.Remove(s.cfg.PidFile)
		}
		s.log.Info("DistCache is now ready to exit, bye bye...")
	})
	return err
}

// cron runs periodic tasks: clock update, active expiry, stats, auto-save,
// client timeouts and replication heartbeats.
func (s *Server) cron() {
	defer s.wg.Done()
	hz := s.cfg.Hz.Load()
	tick := time.NewTicker(time.Second / time.Duration(hz))
	defer tick.Stop()
	var loops int64
	shardCursor := 0
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
		}
		loops++
		now := time.Now()
		s.ks.Clock.Store(uint32(now.Unix()))
		s.ks.LFULogFactor.Store(s.cfg.LFULogFactor.Load())
		s.ks.LFUDecayTime.Store(s.cfg.LFUDecayTime.Load())
		if s.cfg.ActiveExpire.Load() && !s.repl.isReplica() && !s.loading.Load() {
			shardCursor = s.activeExpireCycle(shardCursor)
		}
		curHz := s.cfg.Hz.Load()
		if curHz != hz && curHz > 0 {
			hz = curHz
			tick.Reset(time.Second / time.Duration(hz))
		}
		every := hz / 10
		if every < 1 {
			every = 1
		}
		if loops%every == 0 { // ~100ms
			s.sampleOps()
			s.checkClientTimeouts(now)
		}
		if loops%hz == 0 { // ~1s
			s.checkSaveRules(now)
			s.repl.cron()
			s.prop.cron()
			if s.cfg.MaxMemory.Load() > 0 && !s.repl.isReplica() {
				s.performEvictions()
			}
		}
	}
}

func (s *Server) sampleOps() {
	cur := s.stats.commands.Load()
	d := cur - s.stats.lastOpsCount
	s.stats.lastOpsCount = cur
	s.stats.opsSamples[s.stats.opsSampleIdx%len(s.stats.opsSamples)] = d
	s.stats.opsSampleIdx++
	var sum int64
	for _, v := range s.stats.opsSamples {
		sum += v
	}
	n := int64(len(s.stats.opsSamples))
	if int64(s.stats.opsSampleIdx) < n {
		n = int64(s.stats.opsSampleIdx)
	}
	if n > 0 {
		s.stats.instantaneousOps.Store(sum * 10 / n)
	}
}

func (s *Server) checkClientTimeouts(now time.Time) {
	to := s.cfg.Timeout.Load()
	if to <= 0 {
		return
	}
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for _, c := range s.clients {
		if c.isMaster || c.isReplica || c.inPubSub() || c.blockedNow.Load() {
			continue
		}
		if now.Unix()-c.lastInteraction.Load() > to {
			c.conn.Close()
		}
	}
}

func (s *Server) checkSaveRules(now time.Time) {
	if s.bgsave.Load() || s.aofRewr.Load() {
		return
	}
	dirty := s.stats.dirtySinceSave.Load()
	if dirty == 0 {
		return
	}
	elapsed := now.Unix() - s.stats.lastSaveTime.Load()
	s.cfg.GetString(func(c *config.Config) string {
		for _, r := range c.Save {
			if elapsed >= r[0] && dirty >= r[1] {
				go func() { _ = s.bgSave() }()
				break
			}
		}
		return ""
	})
}

func (s *Server) clientCount() int {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return len(s.clients)
}
