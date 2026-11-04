// Package main implements distcache-bench, a redis-benchmark-style load tool.
//
// It speaks RESP directly over TCP using internal/resp (no third-party
// client) so the same flags and CSV output can drive DistCache and Redis.
package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rabbicse/distcache/internal/resp"
)

// latencyReservoir bounds the number of latency samples kept per test.
const latencyReservoir = 100_000

// DefaultTests is the benchmark set run when no -t flag is given.
var DefaultTests = []string{"set", "get", "incr", "lpush", "rpush", "lpop", "rpop", "sadd", "hset", "zadd"}

// Config describes one benchmark run.
type Config struct {
	Addr     string        // host:port of the server under test
	Clients  int           // parallel connections
	Requests int           // total commands per test (ignored when Duration > 0)
	Pipeline int           // commands per write
	DataSize int           // value size in bytes
	Tests    []string      // test names, see DefaultTests
	KeySpace int           // random keys key:<n> with n < KeySpace; 0 uses one fixed key
	Duration time.Duration // run each test for this long instead of Requests
}

// Result is the outcome of one test. Latencies are per pipeline batch.
type Result struct {
	Test          string
	Requests      int
	Seconds       float64
	OpsPerSec     float64
	P50, P95, P99 time.Duration
}

// Run executes every test in cfg.Tests sequentially and returns one Result each.
func Run(cfg Config) ([]Result, error) {
	cfg = cfg.withDefaults()
	var out []Result
	for _, name := range cfg.Tests {
		gen, ok := generators[name]
		if !ok {
			return nil, fmt.Errorf("unknown test %q", name)
		}
		res, err := runTest(cfg, name, gen)
		if err != nil {
			return nil, fmt.Errorf("test %s: %w", name, err)
		}
		out = append(out, res)
	}
	return out, nil
}

func (c Config) withDefaults() Config {
	c.Clients = max(c.Clients, 1)
	c.Pipeline = max(c.Pipeline, 1)
	if len(c.Tests) == 0 {
		c.Tests = DefaultTests
	}
	return c
}

// cmdGen appends one command to dst; key is already formatted.
type cmdGen func(dst []byte, key string, val string, rnd *rand.Rand) []byte

var generators = map[string]cmdGen{
	"ping": func(dst []byte, _, _ string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "PING")
	},
	"set": func(dst []byte, k, v string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "SET", k, v)
	},
	"get": func(dst []byte, k, _ string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "GET", k)
	},
	"incr": func(dst []byte, k, _ string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "INCR", "counter:"+k)
	},
	"lpush": func(dst []byte, _, v string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "LPUSH", "mylist", v)
	},
	"rpush": func(dst []byte, _, v string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "RPUSH", "mylist", v)
	},
	"lpop": func(dst []byte, _, _ string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "LPOP", "mylist")
	},
	"rpop": func(dst []byte, _, _ string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "RPOP", "mylist")
	},
	"sadd": func(dst []byte, _, _ string, rnd *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "SADD", "myset", "element:"+strconv.Itoa(rnd.IntN(1_000_000)))
	},
	"hset": func(dst []byte, k, v string, _ *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "HSET", "myhash", "element:"+k, v)
	},
	"zadd": func(dst []byte, _, _ string, rnd *rand.Rand) []byte {
		return resp.AppendCommandStrings(dst, "ZADD", "myzset", strconv.Itoa(rnd.IntN(1_000_000)), "element:"+strconv.Itoa(rnd.IntN(1_000_000)))
	},
}

// shared holds state common to all clients of one test.
type shared struct {
	cfg       Config
	gen       cmdGen
	val       string
	remaining atomic.Int64 // commands not yet claimed (count mode)
	deadline  time.Time    // zero in count mode
	done      atomic.Int64 // commands completed
}

// claim reserves up to n commands and returns how many were granted.
func (s *shared) claim(n int) int {
	if !s.deadline.IsZero() {
		if time.Now().After(s.deadline) {
			return 0
		}
		return n
	}
	left := s.remaining.Add(-int64(n))
	if left >= 0 {
		return n
	}
	return max(0, n+int(left))
}

func runTest(cfg Config, name string, gen cmdGen) (Result, error) {
	s := &shared{cfg: cfg, gen: gen, val: strings.Repeat("x", max(cfg.DataSize, 0))}
	s.remaining.Store(int64(cfg.Requests))
	if cfg.Duration > 0 {
		s.deadline = time.Now().Add(cfg.Duration)
	}

	samples := make([][]time.Duration, cfg.Clients)
	errs := make([]error, cfg.Clients)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range cfg.Clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			samples[i], errs[i] = s.client(latencyReservoir/cfg.Clients + 1)
		}()
	}
	wg.Wait()
	secs := time.Since(start).Seconds()
	if err := errors.Join(errs...); err != nil {
		return Result{}, err
	}

	all := slices.Concat(samples...)
	slices.Sort(all)
	n := int(s.done.Load())
	return Result{
		Test: name, Requests: n, Seconds: secs, OpsPerSec: float64(n) / secs,
		P50: percentile(all, 0.50), P95: percentile(all, 0.95), P99: percentile(all, 0.99),
	}, nil
}

// client drives one connection until the shared budget is exhausted. It keeps
// at most capN latency samples by reservoir sampling.
func (s *shared) client(capN int) ([]time.Duration, error) {
	conn, err := net.DialTimeout("tcp", s.cfg.Addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", s.cfg.Addr, err)
	}
	defer conn.Close() // read-only benchmark connection; close error is irrelevant

	rd := resp.NewReader(conn, 64*1024)
	rnd := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	var (
		buf     []byte
		samples = make([]time.Duration, 0, min(capN, 4096))
		seen    int
	)
	for {
		n := s.claim(s.cfg.Pipeline)
		if n == 0 {
			return samples, nil
		}
		buf = buf[:0]
		for range n {
			buf = s.gen(buf, s.key(rnd), s.val, rnd)
		}
		t0 := time.Now()
		if _, err := conn.Write(buf); err != nil {
			return nil, fmt.Errorf("write: %w", err)
		}
		for range n {
			v, err := rd.ReadValue()
			if err != nil {
				return nil, fmt.Errorf("read: %w", err)
			}
			if v.Type == '-' {
				return nil, fmt.Errorf("server error: %s", v.Str)
			}
		}
		d := time.Since(t0)
		s.done.Add(int64(n))

		seen++
		switch {
		case len(samples) < capN:
			samples = append(samples, d)
		default:
			if j := rnd.IntN(seen); j < capN {
				samples[j] = d
			}
		}
	}
}

func (s *shared) key(rnd *rand.Rand) string {
	if s.cfg.KeySpace <= 0 {
		return "key:000000000000"
	}
	return "key:" + strconv.Itoa(rnd.IntN(s.cfg.KeySpace))
}

// percentile returns the q-quantile of sorted (nearest rank).
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := min(int(q*float64(len(sorted))), len(sorted)-1)
	return sorted[i]
}
