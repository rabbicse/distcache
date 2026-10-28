// Package config holds DistCache configuration. It understands redis.conf
// style files and redis-server style command line arguments, and backs the
// CONFIG GET / SET / REWRITE commands.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rabbicse/distcache/internal/ds"
	"github.com/rabbicse/distcache/internal/resp"
)

// Eviction policies.
const (
	PolicyNoEviction = iota
	PolicyAllKeysLRU
	PolicyAllKeysLFU
	PolicyAllKeysRandom
	PolicyVolatileLRU
	PolicyVolatileLFU
	PolicyVolatileRandom
	PolicyVolatileTTL
)

var policyNames = []string{"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random",
	"volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl"}

// PolicyName returns the name of a policy.
func PolicyName(p int32) string { return policyNames[p] }

// Config is the live server configuration. Hot values are atomics so the
// command path can read them without locking.
type Config struct {
	mu sync.RWMutex

	// Startup / string values (guarded by mu).
	Bind            []string
	Port            int
	Dir             string
	DBFilename      string
	AppendFilename  string
	AppendFsync     string
	Save            [][2]int64 // seconds, changes
	ReplicaOf       string
	LogLevel        string
	LogFile         string
	PidFile         string
	ConfigFile      string
	Databases       int
	Shards          int
	UnixSocket      string
	notifyFlagsStr  string
	requirePass     string
	masterAuth      string
	renamed         map[string]string
	maxClientsStr   string
	clientOutputStr string
	saveSeen        bool

	// Hot values.
	MaxMemory           atomic.Int64
	MaxMemoryPolicy     atomic.Int32
	MaxMemorySamples    atomic.Int64
	MaxClients          atomic.Int64
	Timeout             atomic.Int64 // seconds, 0 = never
	TCPKeepAlive        atomic.Int64
	AppendOnly          atomic.Bool
	ReplicaReadOnly     atomic.Bool
	ReplBacklogSize     atomic.Int64
	Hz                  atomic.Int64
	SlowlogSlowerThan   atomic.Int64
	SlowlogMaxLen       atomic.Int64
	NotifyFlags         atomic.Int64
	ProtoMaxBulkLen     atomic.Int64
	LuaTimeLimit        atomic.Int64
	LFULogFactor        atomic.Int64
	LFUDecayTime        atomic.Int64
	ActiveExpire        atomic.Bool
	HashMaxEntries      atomic.Int64
	HashMaxValue        atomic.Int64
	SetMaxIntset        atomic.Int64
	SetMaxEntries       atomic.Int64
	SetMaxValue         atomic.Int64
	ZSetMaxEntries      atomic.Int64
	ZSetMaxValue        atomic.Int64
	ListMaxSize         atomic.Int64
	PubsubBufferLimit   atomic.Int64 // max queued push messages per client
	StopWritesOnBgsave  atomic.Bool
	LazyFreeUserDel     atomic.Bool
	RequirePassSet      atomic.Bool
	BusyReplyThreshold  atomic.Int64
	ClientQueryBufLimit atomic.Int64

	params map[string]*param
	order  []string
}

type param struct {
	name    string
	get     func(c *Config) string
	set     func(c *Config, v string) error
	mutable bool
}

// Keyspace notification flag bits.
const (
	NotifyKeyspace = 1 << iota
	NotifyKeyevent
	NotifyGeneric
	NotifyString
	NotifyList
	NotifySet
	NotifyHash
	NotifyZSet
	NotifyExpired
	NotifyEvicted
	NotifyStream
	NotifyKeyMiss
	NotifyLoaded
	NotifyModule
	NotifyNew
	NotifyAll = NotifyGeneric | NotifyString | NotifyList | NotifySet | NotifyHash | NotifyZSet | NotifyExpired | NotifyEvicted | NotifyStream | NotifyModule
)

// ParseNotifyFlags parses notify-keyspace-events.
func ParseNotifyFlags(s string) (int64, bool) {
	var f int64
	for _, c := range s {
		switch c {
		case 'A':
			f |= NotifyAll
		case 'g':
			f |= NotifyGeneric
		case '$':
			f |= NotifyString
		case 'l':
			f |= NotifyList
		case 's':
			f |= NotifySet
		case 'h':
			f |= NotifyHash
		case 'z':
			f |= NotifyZSet
		case 'x':
			f |= NotifyExpired
		case 'e':
			f |= NotifyEvicted
		case 'K':
			f |= NotifyKeyspace
		case 'E':
			f |= NotifyKeyevent
		case 't':
			f |= NotifyStream
		case 'm':
			f |= NotifyKeyMiss
		case 'd':
			f |= NotifyModule
		case 'n':
			f |= NotifyNew
		default:
			return 0, false
		}
	}
	return f, true
}

// NotifyFlagsString renders flags like Redis.
func NotifyFlagsString(f int64) string {
	var b strings.Builder
	if f&NotifyAll == NotifyAll {
		b.WriteByte('A')
	} else {
		for _, x := range []struct {
			f int64
			c byte
		}{{NotifyGeneric, 'g'}, {NotifyString, '$'}, {NotifyList, 'l'}, {NotifySet, 's'}, {NotifyHash, 'h'},
			{NotifyZSet, 'z'}, {NotifyExpired, 'x'}, {NotifyEvicted, 'e'}, {NotifyStream, 't'}, {NotifyModule, 'd'}} {
			if f&x.f != 0 {
				b.WriteByte(x.c)
			}
		}
	}
	if f&NotifyKeyspace != 0 {
		b.WriteByte('K')
	}
	if f&NotifyKeyevent != 0 {
		b.WriteByte('E')
	}
	if f&NotifyKeyMiss != 0 {
		b.WriteByte('m')
	}
	if f&NotifyNew != 0 {
		b.WriteByte('n')
	}
	return b.String()
}

// Default returns a configuration with Redis-equivalent defaults.
func Default() *Config {
	c := &Config{
		Bind:           []string{"0.0.0.0"},
		Port:           6379,
		Dir:            ".",
		DBFilename:     "dump.dcs",
		AppendFilename: "appendonly.aof",
		AppendFsync:    "everysec",
		Save:           [][2]int64{{3600, 1}, {300, 100}, {60, 10000}},
		LogLevel:       "notice",
		Databases:      16,
		Shards:         256,
		renamed:        map[string]string{},
	}
	c.MaxMemoryPolicy.Store(PolicyNoEviction)
	c.MaxMemorySamples.Store(5)
	c.MaxClients.Store(10000)
	c.TCPKeepAlive.Store(300)
	c.ReplicaReadOnly.Store(true)
	c.ReplBacklogSize.Store(1 << 20)
	c.Hz.Store(10)
	c.SlowlogSlowerThan.Store(10000)
	c.SlowlogMaxLen.Store(128)
	c.ProtoMaxBulkLen.Store(resp.DefaultMaxBulkLen)
	c.LuaTimeLimit.Store(5000)
	c.LFULogFactor.Store(10)
	c.LFUDecayTime.Store(1)
	c.ActiveExpire.Store(true)
	c.HashMaxEntries.Store(128)
	c.HashMaxValue.Store(64)
	c.SetMaxIntset.Store(512)
	c.SetMaxEntries.Store(128)
	c.SetMaxValue.Store(64)
	c.ZSetMaxEntries.Store(128)
	c.ZSetMaxValue.Store(64)
	c.ListMaxSize.Store(-2)
	c.PubsubBufferLimit.Store(100000)
	c.StopWritesOnBgsave.Store(true)
	c.BusyReplyThreshold.Store(5000)
	c.ClientQueryBufLimit.Store(1 << 30)
	c.registerParams()
	return c
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func parseYesNo(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, errors.New("argument must be 'yes' or 'no'")
}

// ParseMemory parses sizes like 100mb, 1gb, 1024.
func ParseMemory(v string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	mul := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"g", 1000 * 1000 * 1000}, {"m", 1000 * 1000}, {"k", 1000}, {"b", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mul = u.m
			s = strings.TrimSuffix(s, u.suf)
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("argument must be a memory value")
	}
	return n * mul, nil
}

func (c *Config) intParam(name string, a *atomic.Int64, min, max int64) {
	c.add(name, true, func(c *Config) string { return strconv.FormatInt(a.Load(), 10) },
		func(c *Config, v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return errors.New("argument couldn't be parsed into an integer")
			}
			if n < min || n > max {
				return fmt.Errorf("argument must be between %d and %d inclusive", min, max)
			}
			a.Store(n)
			return nil
		})
}

func (c *Config) memParam(name string, a *atomic.Int64) {
	c.add(name, true, func(c *Config) string { return strconv.FormatInt(a.Load(), 10) },
		func(c *Config, v string) error {
			n, err := ParseMemory(v)
			if err != nil {
				return err
			}
			a.Store(n)
			return nil
		})
}

func (c *Config) boolParam(name string, a *atomic.Bool) {
	c.add(name, true, func(c *Config) string { return yesno(a.Load()) },
		func(c *Config, v string) error {
			b, err := parseYesNo(v)
			if err != nil {
				return err
			}
			a.Store(b)
			return nil
		})
}

func (c *Config) strParam(name string, mutable bool, p *string) {
	c.add(name, mutable, func(c *Config) string { return *p },
		func(c *Config, v string) error { *p = v; return nil })
}

func (c *Config) add(name string, mutable bool, get func(*Config) string, set func(*Config, string) error) {
	if c.params == nil {
		c.params = map[string]*param{}
	}
	c.params[name] = &param{name: name, get: get, set: set, mutable: mutable}
	c.order = append(c.order, name)
}

// ignoredParams are accepted for redis.conf compatibility but have no effect.
var ignoredParams = []string{"protected-mode", "daemonize", "supervised", "tcp-backlog", "io-threads",
	"io-threads-do-reads", "always-show-logo", "set-proc-title", "rdbcompression", "rdbchecksum",
	"rdb-del-sync-files", "stop-writes-on-bgsave-error-ignored", "replica-serve-stale-data",
	"repl-diskless-sync", "repl-diskless-sync-delay", "repl-diskless-load", "repl-disable-tcp-nodelay",
	"replica-priority", "lazyfree-lazy-eviction", "lazyfree-lazy-expire", "lazyfree-lazy-server-del",
	"replica-lazy-flush", "lazyfree-lazy-user-flush", "oom-score-adj", "oom-score-adj-values",
	"disable-thp", "aof-load-truncated", "aof-use-rdb-preamble", "aof-timestamp-enabled",
	"no-appendfsync-on-rewrite", "auto-aof-rewrite-percentage", "auto-aof-rewrite-min-size",
	"latency-monitor-threshold", "list-compress-depth", "hll-sparse-max-bytes", "stream-node-max-bytes",
	"stream-node-max-entries", "activerehashing", "dynamic-hz", "aof-rewrite-incremental-fsync",
	"rdb-save-incremental-fsync", "jemalloc-bg-thread", "activedefrag", "crash-log-enabled",
	"crash-memcheck-enabled", "syslog-enabled", "syslog-ident", "syslog-facility", "enable-debug-command",
	"enable-module-command", "enable-protected-configs", "locale-collate", "appenddirname",
	"cluster-enabled", "tls-port", "acllog-max-len", "tracking-table-max-keys", "active-expire-effort",
	"close-on-oom", "unixsocketperm", "propagation-error-behavior", "lua-replicate-commands",
	"client-query-buffer-limit-ignored", "replica-announced", "maxmemory-clients", "maxmemory-eviction-tenacity"}

func (c *Config) registerParams() {
	c.add("bind", false, func(c *Config) string { return strings.Join(c.Bind, " ") },
		func(c *Config, v string) error { c.Bind = strings.Fields(v); return nil })
	c.add("port", false, func(c *Config) string { return strconv.Itoa(c.Port) },
		func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return errors.New("argument must be between 0 and 65535 inclusive")
			}
			c.Port = n
			return nil
		})
	c.add("databases", false, func(c *Config) string { return strconv.Itoa(c.Databases) },
		func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1<<20 {
				return errors.New("argument must be between 1 and 1048576 inclusive")
			}
			c.Databases = n
			return nil
		})
	c.add("shards", false, func(c *Config) string { return strconv.Itoa(c.Shards) },
		func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65536 || n&(n-1) != 0 {
				return errors.New("argument must be a power of two between 1 and 65536")
			}
			c.Shards = n
			return nil
		})
	c.strParam("dir", true, &c.Dir)
	c.strParam("dbfilename", true, &c.DBFilename)
	c.strParam("appendfilename", false, &c.AppendFilename)
	c.strParam("logfile", false, &c.LogFile)
	c.strParam("pidfile", false, &c.PidFile)
	c.strParam("unixsocket", false, &c.UnixSocket)
	c.add("loglevel", true, func(c *Config) string { return c.LogLevel },
		func(c *Config, v string) error {
			switch v {
			case "debug", "verbose", "notice", "warning", "nothing":
				c.LogLevel = v
				return nil
			}
			return errors.New("argument(s) must be one of the following: debug, verbose, notice, warning, nothing")
		})
	c.add("appendfsync", true, func(c *Config) string { return c.AppendFsync },
		func(c *Config, v string) error {
			v = strings.ToLower(v)
			switch v {
			case "always", "everysec", "no":
				c.AppendFsync = v
				return nil
			}
			return errors.New("argument(s) must be one of the following: always, everysec, no")
		})
	c.boolParam("appendonly", &c.AppendOnly)
	c.add("save", true, func(c *Config) string {
		parts := []string{}
		for _, s := range c.Save {
			parts = append(parts, strconv.FormatInt(s[0], 10), strconv.FormatInt(s[1], 10))
		}
		return strings.Join(parts, " ")
	}, func(c *Config, v string) error {
		f := strings.Fields(v)
		if len(f)%2 != 0 {
			return errors.New("Invalid save parameters")
		}
		var rules [][2]int64
		for i := 0; i < len(f); i += 2 {
			a, e1 := strconv.ParseInt(f[i], 10, 64)
			b, e2 := strconv.ParseInt(f[i+1], 10, 64)
			if e1 != nil || e2 != nil || a < 1 || b < 0 {
				return errors.New("Invalid save parameters")
			}
			rules = append(rules, [2]int64{a, b})
		}
		c.Save = rules
		return nil
	})
	c.memParam("maxmemory", &c.MaxMemory)
	c.add("maxmemory-policy", true, func(c *Config) string { return PolicyName(c.MaxMemoryPolicy.Load()) },
		func(c *Config, v string) error {
			for i, n := range policyNames {
				if strings.EqualFold(n, v) {
					c.MaxMemoryPolicy.Store(int32(i))
					return nil
				}
			}
			return errors.New("argument(s) must be one of the following: " + strings.Join(policyNames, ", "))
		})
	c.intParam("maxmemory-samples", &c.MaxMemorySamples, 1, 64)
	c.intParam("maxclients", &c.MaxClients, 1, 1<<31)
	c.intParam("timeout", &c.Timeout, 0, 1<<31)
	c.intParam("tcp-keepalive", &c.TCPKeepAlive, 0, 1<<31)
	c.intParam("hz", &c.Hz, 1, 500)
	c.intParam("slowlog-log-slower-than", &c.SlowlogSlowerThan, -1, 1<<62)
	c.intParam("slowlog-max-len", &c.SlowlogMaxLen, 0, 1<<31)
	c.memParam("proto-max-bulk-len", &c.ProtoMaxBulkLen)
	c.memParam("client-query-buffer-limit", &c.ClientQueryBufLimit)
	c.intParam("lua-time-limit", &c.LuaTimeLimit, 0, 1<<62)
	c.intParam("busy-reply-threshold", &c.BusyReplyThreshold, 0, 1<<62)
	c.intParam("lfu-log-factor", &c.LFULogFactor, 0, 1<<31)
	c.intParam("lfu-decay-time", &c.LFUDecayTime, 0, 1<<31)
	c.memParam("repl-backlog-size", &c.ReplBacklogSize)
	c.boolParam("replica-read-only", &c.ReplicaReadOnly)
	c.boolParam("stop-writes-on-bgsave-error", &c.StopWritesOnBgsave)
	c.boolParam("lazyfree-lazy-user-del", &c.LazyFreeUserDel)
	c.boolParam("active-expire-enabled", &c.ActiveExpire)
	c.intParam("hash-max-listpack-entries", &c.HashMaxEntries, 0, 1<<62)
	c.intParam("hash-max-listpack-value", &c.HashMaxValue, 0, 1<<62)
	c.intParam("hash-max-ziplist-entries", &c.HashMaxEntries, 0, 1<<62)
	c.intParam("hash-max-ziplist-value", &c.HashMaxValue, 0, 1<<62)
	c.intParam("set-max-intset-entries", &c.SetMaxIntset, 0, 1<<62)
	c.intParam("set-max-listpack-entries", &c.SetMaxEntries, 0, 1<<62)
	c.intParam("set-max-listpack-value", &c.SetMaxValue, 0, 1<<62)
	c.intParam("zset-max-listpack-entries", &c.ZSetMaxEntries, 0, 1<<62)
	c.intParam("zset-max-listpack-value", &c.ZSetMaxValue, 0, 1<<62)
	c.intParam("zset-max-ziplist-entries", &c.ZSetMaxEntries, 0, 1<<62)
	c.intParam("zset-max-ziplist-value", &c.ZSetMaxValue, 0, 1<<62)
	c.intParam("list-max-listpack-size", &c.ListMaxSize, -5, 1<<62)
	c.intParam("list-max-ziplist-size", &c.ListMaxSize, -5, 1<<62)
	c.intParam("pubsub-max-queued-messages", &c.PubsubBufferLimit, 1, 1<<40)
	c.add("notify-keyspace-events", true, func(c *Config) string { return NotifyFlagsString(c.NotifyFlags.Load()) },
		func(c *Config, v string) error {
			f, ok := ParseNotifyFlags(v)
			if !ok {
				return errors.New("Invalid event class character. Use 'Ag$lshzxeKEtmdn'.")
			}
			c.NotifyFlags.Store(f)
			return nil
		})
	c.add("requirepass", true, func(c *Config) string { return c.requirePass },
		func(c *Config, v string) error {
			c.requirePass = v
			c.RequirePassSet.Store(v != "")
			return nil
		})
	c.strParam("masterauth", true, &c.masterAuth)
	c.add("replicaof", false, func(c *Config) string { return c.ReplicaOf },
		func(c *Config, v string) error { c.ReplicaOf = v; return nil })
	c.add("slaveof", false, func(c *Config) string { return c.ReplicaOf },
		func(c *Config, v string) error { c.ReplicaOf = v; return nil })
	c.add("rename-command", false, func(c *Config) string { return "" },
		func(c *Config, v string) error {
			f := strings.Fields(v)
			if len(f) != 2 {
				return errors.New("rename-command needs two arguments")
			}
			c.renamed[strings.ToLower(f[0])] = strings.Trim(f[1], "\"")
			return nil
		})
	c.add("client-output-buffer-limit", true, func(c *Config) string {
		if c.clientOutputStr == "" {
			return "normal 0 0 0 slave 268435456 67108864 60 pubsub 33554432 8388608 60"
		}
		return c.clientOutputStr
	}, func(c *Config, v string) error { c.clientOutputStr = v; return nil })
	for _, n := range ignoredParams {
		n := n
		var val string
		c.add(n, true, func(c *Config) string { return val }, func(c *Config, v string) error { val = v; return nil })
	}
}

// RequirePass returns the configured password.
func (c *Config) RequirePass() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.requirePass
}

// MasterAuth returns masterauth.
func (c *Config) MasterAuth() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.masterAuth
}

// Renamed returns the rename-command map.
func (c *Config) Renamed() map[string]string { return c.renamed }

// GetString returns any string field under lock.
func (c *Config) GetString(fn func(c *Config) string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fn(c)
}

// Set changes a parameter. When startup is false immutable params fail.
func (c *Config) Set(name, value string, startup bool) error {
	name = strings.ToLower(name)
	p, ok := c.params[name]
	if !ok {
		return fmt.Errorf("Unknown option or number of arguments for CONFIG SET - '%s'", name)
	}
	if !startup && !p.mutable {
		return fmt.Errorf("CONFIG SET failed (possibly related to argument '%s') - can't set immutable config", name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := p.set(c, value); err != nil {
		if startup {
			return fmt.Errorf("'%s %s': %v", name, value, err)
		}
		return fmt.Errorf("CONFIG SET failed (possibly related to argument '%s') - %v", name, err)
	}
	return nil
}

// Get returns name/value pairs matching a glob pattern.
func (c *Config) Get(pattern string) [][2]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pattern = strings.ToLower(pattern)
	var out [][2]string
	seen := map[string]bool{}
	names := append([]string(nil), c.order...)
	sort.Strings(names)
	for _, n := range names {
		if seen[n] || n == "rename-command" {
			continue
		}
		if ds.Match(pattern, n, true) {
			seen[n] = true
			out = append(out, [2]string{n, c.params[n].get(c)})
		}
	}
	return out
}

// Has reports whether a parameter exists.
func (c *Config) Has(name string) bool { _, ok := c.params[strings.ToLower(name)]; return ok }

// LoadFile parses a redis.conf style file.
func (c *Config) LoadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	c.ConfigFile = path
	sc := bufio.NewScanner(f)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		args, ok := resp.SplitArgs(line)
		if !ok || len(args) == 0 {
			return fmt.Errorf("%s:%d: unbalanced quotes", path, ln)
		}
		if strings.EqualFold(args[0], "include") && len(args) == 2 {
			if err := c.LoadFile(args[1]); err != nil {
				return err
			}
			continue
		}
		if err := c.applyDirective(args); err != nil {
			return fmt.Errorf("%s:%d: %v", path, ln, err)
		}
	}
	return sc.Err()
}

func (c *Config) applyDirective(args []string) error {
	name := strings.ToLower(args[0])
	val := strings.Join(args[1:], " ")
	if name == "save" && c.saveSeen == false {
		c.saveSeen = true
		c.Save = nil
		if val == "" || val == `""` {
			return nil
		}
		val = strings.Join(append(strings.Fields(c.saveString()), strings.Fields(val)...), " ")
	} else if name == "save" {
		val = strings.Join(append(strings.Fields(c.saveString()), strings.Fields(val)...), " ")
	}
	return c.Set(name, val, true)
}

func (c *Config) saveString() string { return c.params["save"].get(c) }

// ParseArgs applies redis-server style arguments: an optional config file
// path followed by --name value pairs. Legacy -listenAddr is supported.
func (c *Config) ParseArgs(args []string) error {
	i := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if err := c.LoadFile(args[0]); err != nil {
			return err
		}
		i = 1
	}
	for i < len(args) {
		a := args[i]
		if a == "-listenAddr" || a == "--listenAddr" {
			if i+1 >= len(args) {
				return errors.New("missing value for -listenAddr")
			}
			host, port, ok := strings.Cut(args[i+1], ":")
			if !ok {
				return errors.New("-listenAddr must be host:port")
			}
			if host != "" {
				c.Bind = []string{host}
			}
			if err := c.Set("port", port, true); err != nil {
				return err
			}
			i += 2
			continue
		}
		if !strings.HasPrefix(a, "--") {
			return fmt.Errorf("unexpected argument %q", a)
		}
		name := strings.TrimPrefix(a, "--")
		var vals []string
		i++
		for i < len(args) && !strings.HasPrefix(args[i], "--") {
			vals = append(vals, args[i])
			i++
		}
		if err := c.applyDirective(append([]string{name}, vals...)); err != nil {
			return err
		}
	}
	return nil
}

// Rewrite writes the current configuration to the config file.
func (c *Config) Rewrite() error {
	if c.ConfigFile == "" {
		return errors.New("The server is running without a config file")
	}
	pairs := c.Get("*")
	var b strings.Builder
	b.WriteString("# Generated by DistCache CONFIG REWRITE\n")
	for _, p := range pairs {
		if p[0] == "slaveof" || strings.Contains(p[0], "ziplist") {
			continue
		}
		if p[1] == "" && p[0] != "save" {
			continue
		}
		b.WriteString(p[0])
		b.WriteByte(' ')
		if p[0] == "save" && p[1] == "" {
			b.WriteString(`""`)
		} else {
			b.WriteString(strconv.Quote(p[1]))
		}
		b.WriteByte('\n')
	}
	tmp := c.ConfigFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.ConfigFile)
}
