// Microbenchmarks for the command path: in-process Exec, raw dispatch,
// parallel shard contention and a pipelined TCP client. Baseline numbers live
// in bench/results/microbench-baseline.txt (make microbench).
package server_test

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/internal/testutil"
)

const benchKeyCount = 10_000

// benchKeys returns "key:<i>" for i < benchKeyCount.
func benchKeys() []string {
	keys := make([]string, benchKeyCount)
	for i := range keys {
		keys[i] = "key:" + strconv.Itoa(i)
	}
	return keys
}

// startBenchServer starts a server preloaded with every benchKeys key.
func startBenchServer(b *testing.B) (*server.Server, string, []string) {
	b.Helper()
	s, addr := testutil.StartServer(b, nil)
	keys := benchKeys()
	ss := s.NewSession()
	b.Cleanup(ss.Close)
	for _, k := range keys {
		if _, err := ss.Exec("SET", k, "xxx"); err != nil {
			b.Fatal(err)
		}
	}
	return s, addr, keys
}

func BenchmarkExecGet(b *testing.B) {
	s, _, keys := startBenchServer(b)
	ss := s.NewSession()
	b.Cleanup(ss.Close)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, err := ss.Exec("GET", keys[i%len(keys)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExecSet(b *testing.B) {
	s, _, keys := startBenchServer(b)
	ss := s.NewSession()
	b.Cleanup(ss.Close)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, err := ss.Exec("SET", keys[i%len(keys)], "yyy"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProcessCommandGet(b *testing.B) {
	s, _, _ := startBenchServer(b)
	run := s.DispatchFunc([][]byte{[]byte("GET"), []byte("key:42")})
	b.ReportAllocs()
	for b.Loop() {
		run()
	}
}

// parallelExec runs cmd(i) on one Session per goroutine.
func parallelExec(b *testing.B, s *server.Server, cmd func(i int) []string) {
	b.Helper()
	var seed atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ss := s.NewSession()
		defer ss.Close()
		i := int(seed.Add(7919))
		for pb.Next() {
			if _, err := ss.Exec(cmd(i)...); err != nil {
				b.Error(err)
				return
			}
			i++
		}
	})
}

func BenchmarkParallelGet(b *testing.B) {
	s, _, keys := startBenchServer(b)
	parallelExec(b, s, func(i int) []string { return []string{"GET", keys[i%len(keys)]} })
}

func BenchmarkParallelSet(b *testing.B) {
	s, _, keys := startBenchServer(b)
	parallelExec(b, s, func(i int) []string { return []string{"SET", keys[i%len(keys)], "yyy"} })
}

// BenchmarkParallelHotKeyGet has every goroutine read one key, exposing
// contention on that key's shard lock and its LRU/LFU touch.
func BenchmarkParallelHotKeyGet(b *testing.B) {
	s, _, _ := startBenchServer(b)
	parallelExec(b, s, func(int) []string { return []string{"GET", "key:42"} })
}

// BenchmarkPipeline16 sends 16 GETs per write over one TCP connection; one
// op is one command.
func BenchmarkPipeline16(b *testing.B) {
	_, addr, keys := startBenchServer(b)
	c := testutil.MustDial(b, addr)
	var batch []byte
	for i := range 16 {
		batch = resp.AppendCommandStrings(batch, "GET", keys[i])
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n += 16 {
		if _, err := c.Conn.Write(batch); err != nil {
			b.Fatal(err)
		}
		for range 16 {
			if _, err := c.Read(); err != nil {
				b.Fatal(err)
			}
		}
	}
}
