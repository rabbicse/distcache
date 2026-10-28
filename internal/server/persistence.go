package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rabbicse/distcache/internal/storage"
)

func (s *Server) snapshotPath() string { return filepath.Join(s.cfg.Dir, s.cfg.DBFilename) }

// loadData restores the dataset at startup: from the AOF when appendonly is
// enabled (like Redis), otherwise from the snapshot file.
func (s *Server) loadData() error {
	s.loading.Store(true)
	defer s.loading.Store(false)
	start := time.Now()
	if s.cfg.AppendOnly.Load() {
		if _, err := os.Stat(s.prop.aofPath()); err == nil {
			if err := s.loadAOF(s.prop.aofPath()); err != nil {
				return err
			}
			s.log.Info("dataset loaded", "source", "aof", "seconds", time.Since(start).Seconds())
			return nil
		}
	}
	f, err := os.Open(s.snapshotPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	n, err := s.loadSnapshot(bufio.NewReaderSize(f, 1<<16))
	if err != nil {
		return fmt.Errorf("loading %s: %w", s.snapshotPath(), err)
	}
	s.log.Info("DB loaded from disk", "keys", n, "seconds", time.Since(start).Seconds())
	return nil
}

func (s *Server) loadSnapshot(r *bufio.Reader) (int, error) {
	n := 0
	err := storage.DecodeWithAux(r, func(db int, key string, v any, exp int64) error {
		if s.loadKey(db, key, v, exp) {
			n++
		}
		return nil
	}, s.loadAux)
	return n, err
}

// loadKey inserts a decoded key; caller must ensure exclusive access (startup
// or all shards locked). Already expired keys are skipped on masters.
func (s *Server) loadKey(db int, key string, v any, exp int64) bool {
	if db >= s.ks.NumDB {
		return false
	}
	now := time.Now().UnixMilli()
	if exp != 0 && exp <= now && !s.repl.isReplica() {
		return false
	}
	d := storage.DB{KS: s.ks, Idx: db, Now: now, Write: true}
	e := s.ks.NewEntry(v)
	e.ExpireAt = exp
	d.Set(key, e)
	return true
}

// writeSnapshot writes a point-in-time snapshot to w. cut runs at the
// snapshot instant with all shards locked.
func (s *Server) writeSnapshot(w io.Writer, cut func()) error {
	s.ks.BeginSnapshot(cut)
	enc := storage.NewEncoder(w)
	for i := 0; i < s.ks.NumShards(); i++ {
		s.ks.SnapshotShard(i, func(db int, key string, e *storage.Entry) { enc.WriteEntry(db, key, e) })
	}
	s.ks.EndSnapshot()
	for _, code := range s.scripts.libraryCodes() {
		enc.WriteAux("function", []byte(code))
	}
	return enc.Close()
}

// loadAux restores auxiliary snapshot records.
func (s *Server) loadAux(name string, data []byte) error {
	if name == "function" {
		if _, err := s.scripts.functionLoad(string(data), true); err != nil {
			s.log.Warn("failed to restore function library", "err", err)
		}
	}
	return nil
}

// saveSync performs SAVE. Thanks to copy-on-write snapshots, unlike Redis'
// SAVE it does not block other clients while writing.
func (s *Server) saveSync() error {
	path := s.snapshotPath()
	tmp := filepath.Join(s.cfg.Dir, fmt.Sprintf("temp-%d.dcs", time.Now().UnixNano()))
	f, err := os.Create(tmp)
	if err != nil {
		s.stats.lastBgsaveOK.Store(false)
		return err
	}
	start := time.Now()
	dirtyBefore := s.stats.dirtySinceSave.Load()
	err = s.writeSnapshot(f, nil)
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		s.stats.lastBgsaveOK.Store(false)
		s.log.Warn("failed saving the DB", "err", err)
		return err
	}
	s.stats.dirtySinceSave.Add(-dirtyBefore)
	s.stats.lastSaveTime.Store(time.Now().Unix())
	s.stats.lastBgsaveOK.Store(true)
	s.stats.lastBgsaveSeconds.Store(int64(time.Since(start).Seconds()))
	s.log.Info("DB saved on disk", "path", path, "seconds", time.Since(start).Seconds())
	return nil
}

var errBgsaveInProgress = errors.New("Background save already in progress")

// bgSave runs saveSync in the background.
func (s *Server) bgSave() error {
	if !s.bgsave.CompareAndSwap(false, true) {
		return errBgsaveInProgress
	}
	go func() {
		defer s.bgsave.Store(false)
		_ = s.saveSync()
	}()
	return nil
}
