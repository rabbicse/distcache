package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseArgs_Pairs(t *testing.T) {
	c := Default()
	err := c.ParseArgs([]string{"--port", "6380", "--appendonly", "yes", "--save", "60", "1", "300", "10"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 6380 {
		t.Errorf("port got %d, want 6380", c.Port)
	}
	if !c.AppendOnly.Load() {
		t.Error("appendonly not enabled")
	}
	if want := [][2]int64{{60, 1}, {300, 10}}; !reflect.DeepEqual(c.Save, want) {
		t.Errorf("save got %v, want %v", c.Save, want)
	}
}

func TestParseArgs_ConfigFileThenOverride(t *testing.T) {
	path := writeFile(t, t.TempDir(), "redis.conf", "port 7000\n")
	c := Default()
	if err := c.ParseArgs([]string{path, "--port", "7001"}); err != nil {
		t.Fatal(err)
	}
	if c.Port != 7001 {
		t.Errorf("port got %d, want 7001", c.Port)
	}
	if c.ConfigFile != path {
		t.Errorf("ConfigFile got %q, want %q", c.ConfigFile, path)
	}
	c2 := Default()
	if err := c2.ParseArgs([]string{path}); err != nil || c2.Port != 7000 {
		t.Errorf("file only got port %d err %v, want 7000", c2.Port, err)
	}
}

func TestParseArgs_Errors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing file", []string{filepath.Join(t.TempDir(), "nope.conf")}, ""},
		{"single dash flag", []string{"-port", "1"}, "unexpected argument"},
		{"bad port", []string{"--port", "99999"}, "between 0 and 65535"},
		{"unknown option", []string{"--bogus", "1"}, "Unknown option"},
		{"listenAddr missing value", []string{"-listenAddr"}, "missing value"},
		{"listenAddr no port", []string{"-listenAddr", "localhost"}, "host:port"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Default().ParseArgs(tc.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLegacyListenAddr(t *testing.T) {
	c := Default()
	if err := c.ParseArgs([]string{"-listenAddr", "127.0.0.1:6400"}); err != nil {
		t.Fatal(err)
	}
	if c.Port != 6400 || !slices.Equal(c.Bind, []string{"127.0.0.1"}) {
		t.Errorf("got bind=%v port=%d", c.Bind, c.Port)
	}
	c = Default()
	if err := c.ParseArgs([]string{"--listenAddr", ":6401"}); err != nil {
		t.Fatal(err)
	}
	if c.Port != 6401 || !slices.Equal(c.Bind, []string{"0.0.0.0"}) {
		t.Errorf("empty host should keep bind: bind=%v port=%d", c.Bind, c.Port)
	}
}

func TestLoadFile_Include(t *testing.T) {
	dir := t.TempDir()
	inc := writeFile(t, dir, "inc.conf", "port 7100\nmaxclients 42\n")
	main := writeFile(t, dir, "main.conf", "# comment\n\ninclude "+inc+"\nhz 20\nrequirepass \"se cret\"\n")
	c := Default()
	if err := c.LoadFile(main); err != nil {
		t.Fatal(err)
	}
	if c.Port != 7100 || c.MaxClients.Load() != 42 || c.Hz.Load() != 20 {
		t.Errorf("got port=%d maxclients=%d hz=%d", c.Port, c.MaxClients.Load(), c.Hz.Load())
	}
	if c.RequirePass() != "se cret" || !c.RequirePassSet.Load() {
		t.Errorf("requirepass got %q", c.RequirePass())
	}
}

func TestLoadFile_IncludeKeepsMainConfigFile(t *testing.T) {
	dir := t.TempDir()
	inc := writeFile(t, dir, "inc.conf", "port 7100\n")
	mainPath := writeFile(t, dir, "main.conf", "include "+inc+"\nhz 20\n")
	c := Default()
	if err := c.LoadFile(mainPath); err != nil {
		t.Fatal(err)
	}
	if c.ConfigFile != mainPath {
		t.Errorf("ConfigFile got %q, want %q", c.ConfigFile, mainPath)
	}
}

func TestLoadFile_Errors(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"unbalanced quotes", "requirepass \"abc\n", "unbalanced quotes"},
		{"bad value", "port abc\n", "main.conf:1"},
		{"unknown directive", "nonsense 1\n", "Unknown option"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, dir, "main.conf", tc.body)
			err := Default().LoadFile(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want substring %q", err, tc.want)
			}
		})
	}
	if err := Default().LoadFile(filepath.Join(dir, "missing.conf")); err == nil {
		t.Error("missing file accepted")
	}
	p := writeFile(t, dir, "bad-include.conf", "include "+filepath.Join(dir, "missing.conf")+"\n")
	if err := Default().LoadFile(p); err == nil {
		t.Error("missing include accepted")
	}
}

func TestSaveEmptyStringDisables(t *testing.T) {
	p := writeFile(t, t.TempDir(), "redis.conf", "save \"\"\n")
	c := Default()
	if err := c.LoadFile(p); err != nil {
		t.Fatal(err)
	}
	if c.Save != nil {
		t.Errorf("Save got %v, want nil", c.Save)
	}
	c = Default()
	if err := c.ParseArgs([]string{"--save", ""}); err != nil {
		t.Fatal(err)
	}
	if c.Save != nil {
		t.Errorf("--save \"\" got %v, want nil", c.Save)
	}
}

func TestSaveAccumulates(t *testing.T) {
	p := writeFile(t, t.TempDir(), "redis.conf", "save 900 1\nsave 300 10\n")
	c := Default()
	if err := c.LoadFile(p); err != nil {
		t.Fatal(err)
	}
	if want := [][2]int64{{900, 1}, {300, 10}}; !reflect.DeepEqual(c.Save, want) {
		t.Errorf("got %v, want %v", c.Save, want)
	}
}

func TestSet_SaveValidation(t *testing.T) {
	for _, v := range []string{"60", "0 1", "a b", "60 -1"} {
		t.Run(v, func(t *testing.T) {
			if err := Default().Set("save", v, false); err == nil {
				t.Error("invalid save accepted")
			}
		})
	}
}

func TestImmutableParamRejectedAtRuntime(t *testing.T) {
	c := Default()
	err := c.Set("port", "1", false)
	if err == nil || !strings.Contains(err.Error(), "can't set immutable config") {
		t.Fatalf("got %v, want immutable error", err)
	}
	if c.Port != 6379 {
		t.Errorf("port changed to %d", c.Port)
	}
	if err := c.Set("port", "1234", true); err != nil || c.Port != 1234 {
		t.Errorf("startup set got err=%v port=%d", err, c.Port)
	}
	if err := c.Set("hz", "50", false); err != nil || c.Hz.Load() != 50 {
		t.Errorf("mutable set got err=%v hz=%d", err, c.Hz.Load())
	}
}

func TestSet_ErrorsAndCase(t *testing.T) {
	c := Default()
	tests := []struct {
		name, key, val string
		startup        bool
		want           string
	}{
		{"unknown", "nope", "1", false, "Unknown option"},
		{"hz range", "hz", "0", false, "between 1 and 500"},
		{"hz not int", "hz", "x", false, "couldn't be parsed"},
		{"bool bad", "appendonly", "maybe", true, "'yes' or 'no'"},
		{"loglevel bad", "loglevel", "loud", false, "one of the following"},
		{"policy bad", "maxmemory-policy", "x", false, "one of the following"},
		{"fsync bad", "appendfsync", "sometimes", false, "always, everysec, no"},
		{"shards pow2", "shards", "3", true, "power of two"},
		{"databases range", "databases", "0", true, "between 1"},
		{"notify bad", "notify-keyspace-events", "Q", false, "Invalid event class"},
		{"maxmemory bad", "maxmemory", "-1", false, "memory value"},
		{"rename args", "rename-command", "only", true, "two arguments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Set(tc.key, tc.val, tc.startup)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want substring %q", err, tc.want)
			}
		})
	}
	if err := c.Set("HZ", "30", false); err != nil || c.Hz.Load() != 30 {
		t.Errorf("name should be case-insensitive: %v", err)
	}
}

func TestSet_TypedParams(t *testing.T) {
	c := Default()
	sets := [][2]string{
		{"maxmemory", "100mb"}, {"maxmemory-policy", "ALLKEYS-LRU"}, {"appendfsync", "ALWAYS"},
		{"loglevel", "debug"}, {"dir", "/data"}, {"requirepass", "pw"}, {"masterauth", "m"},
		{"bind", "127.0.0.1 ::1"}, {"replicaof", "h 1"}, {"rename-command", `FLUSHALL "x"`},
		{"client-output-buffer-limit", "normal 1 1 1"}, {"stop-writes-on-bgsave-error", "no"},
	}
	for _, s := range sets {
		if err := c.Set(s[0], s[1], true); err != nil {
			t.Fatalf("Set(%s): %v", s[0], err)
		}
	}
	if c.MaxMemory.Load() != 100<<20 || PolicyName(c.MaxMemoryPolicy.Load()) != "allkeys-lru" {
		t.Errorf("memory settings wrong: %d", c.MaxMemory.Load())
	}
	if c.AppendFsync != "always" || c.LogLevel != "debug" || c.Dir != "/data" {
		t.Errorf("string settings wrong: %q %q %q", c.AppendFsync, c.LogLevel, c.Dir)
	}
	if c.MasterAuth() != "m" || c.RequirePass() != "pw" || !slices.Equal(c.Bind, []string{"127.0.0.1", "::1"}) {
		t.Error("auth/bind wrong")
	}
	if c.Renamed()["flushall"] != "x" || c.ReplicaOf != "h 1" || c.StopWritesOnBgsave.Load() {
		t.Error("renamed/replicaof/bool wrong")
	}
	if got := c.GetString(func(c *Config) string { return c.Dir }); got != "/data" {
		t.Errorf("GetString got %q", got)
	}
	if err := c.Set("requirepass", "", false); err != nil || c.RequirePassSet.Load() {
		t.Error("clearing requirepass failed")
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1gb", 1 << 30, false},
		{"100mb", 100 << 20, false},
		{"1k", 1000, false},
		{"1kb", 1024, false},
		{"2m", 2_000_000, false},
		{"3g", 3_000_000_000, false},
		{"12", 12, false},
		{"12b", 12, false},
		{" 1GB ", 1 << 30, false},
		{"0", 0, false},
		{"-1", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{"1.5gb", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseMemory(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("got %d,%v, want %d, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestNotifyFlagsRoundTrip(t *testing.T) {
	f, ok := ParseNotifyFlags("KEA")
	if !ok {
		t.Fatal("parse failed")
	}
	if got := NotifyFlagsString(f); got != "AKE" {
		t.Errorf("got %q, want AKE", got)
	}
	f, _ = ParseNotifyFlags("Kgx$mn")
	if got := NotifyFlagsString(f); got != "g$xKmn" {
		t.Errorf("got %q, want g$xKmn", got)
	}
	if _, ok := ParseNotifyFlags("Z"); ok {
		t.Error("invalid flag accepted")
	}
	if f, ok := ParseNotifyFlags(""); !ok || NotifyFlagsString(f) != "" {
		t.Error("empty flags wrong")
	}
	for _, s := range []string{"lshzet", "dKE"} {
		f, _ := ParseNotifyFlags(s)
		f2, _ := ParseNotifyFlags(NotifyFlagsString(f))
		if f != f2 {
			t.Errorf("%q not stable", s)
		}
	}
}

func TestIgnoredParamsAccepted(t *testing.T) {
	c := Default()
	if err := c.Set("protected-mode", "yes", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("protected-mode", "no", false); err != nil {
		t.Fatalf("ignored params should be mutable: %v", err)
	}
	p := writeFile(t, t.TempDir(), "redis.conf", "protected-mode yes\ndaemonize no\n")
	if err := Default().LoadFile(p); err != nil {
		t.Errorf("load got %v", err)
	}
	if got := c.Get("protected-mode"); len(got) != 1 || got[0][1] != "no" {
		t.Errorf("Get got %v", got)
	}
}

func TestGet_PatternAndHelpers(t *testing.T) {
	c := Default()
	got := c.Get("MAXMEMORY*")
	var names []string
	for _, p := range got {
		names = append(names, p[0])
	}
	for _, want := range []string{"maxmemory", "maxmemory-policy", "maxmemory-samples"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing %s in %v", want, names)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("not sorted: %v", names)
	}
	if g := c.Get("rename-command"); len(g) != 0 {
		t.Errorf("rename-command should be hidden, got %v", g)
	}
	if v := c.Get("port"); len(v) != 1 || v[0][1] != "6379" {
		t.Errorf("port got %v", v)
	}
	if !c.Has("Port") || c.Has("nope") {
		t.Error("Has wrong")
	}
	if v := c.Get("save"); v[0][1] != "3600 1 300 100 60 10000" {
		t.Errorf("default save got %q", v[0][1])
	}
}

func TestRewrite_NoConfigFile(t *testing.T) {
	if err := Default().Rewrite(); err == nil {
		t.Error("rewrite without file accepted")
	}
}

func TestRewriteRoundTrip(t *testing.T) {
	path := writeFile(t, t.TempDir(), "redis.conf", "port 7000\n")
	c := Default()
	if err := c.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]string{
		{"maxmemory", "64mb"}, {"maxmemory-policy", "volatile-ttl"}, {"appendonly", "yes"},
		{"save", "120 5 30 50"}, {"requirepass", "p w\"x"}, {"notify-keyspace-events", "KEA"},
		{"hz", "25"}, {"dir", "/some dir"}, {"loglevel", "warning"},
	} {
		if err := c.Set(s[0], s[1], true); err != nil {
			t.Fatalf("Set(%s): %v", s[0], err)
		}
	}
	if err := c.Rewrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
	fresh := Default()
	if err := fresh.LoadFile(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, want := fresh.Get("*"), c.Get("*"); !reflect.DeepEqual(got, want) {
		for i := range want {
			if i < len(got) && got[i] != want[i] {
				t.Errorf("mismatch: got %v, want %v", got[i], want[i])
			}
		}
		t.Fatalf("round trip differs (%d vs %d params)", len(got), len(want))
	}
}

func TestRewriteRoundTrip_SaveDisabled(t *testing.T) {
	path := writeFile(t, t.TempDir(), "redis.conf", "save \"\"\n")
	c := Default()
	if err := c.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if err := c.Rewrite(); err != nil {
		t.Fatal(err)
	}
	fresh := Default()
	if err := fresh.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if fresh.Save != nil {
		t.Errorf("Save got %v, want nil", fresh.Save)
	}
}

func TestRewrite_UnwritableDir(t *testing.T) {
	c := Default()
	c.ConfigFile = filepath.Join(t.TempDir(), "missing-dir", "redis.conf")
	if err := c.Rewrite(); err == nil {
		t.Error("expected write error")
	}
}

func TestPolicyName(t *testing.T) {
	if PolicyName(PolicyVolatileTTL) != "volatile-ttl" || PolicyName(PolicyNoEviction) != "noeviction" {
		t.Error("PolicyName wrong")
	}
}

func TestDefault_Values(t *testing.T) {
	c := Default()
	if c.Port != 6379 || c.Databases != 16 || c.LogLevel != "notice" || !c.ReplicaReadOnly.Load() {
		t.Errorf("unexpected defaults: %d %d %q", c.Port, c.Databases, c.LogLevel)
	}
	if c.Get("client-output-buffer-limit")[0][1] == "" {
		t.Error("client-output-buffer-limit default empty")
	}
}
