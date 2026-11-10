package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/server"
)

// consoleEnv is a console backed by a real in-process server with one
// logged-in browser session.
type consoleEnv struct {
	c   *Console
	srv *server.Server
	ck  *http.Cookie
	s   *session
	lb  *logBuf
}

func newConsoleEnv(t *testing.T) *consoleEnv {
	t.Helper()
	cfg := config.Default()
	if err := cfg.Set("requirepass", testPass, true); err != nil {
		t.Fatal(err)
	}
	srv := server.New(cfg, nil)
	log, lb := newBufLogger()
	c, err := New(Options{Server: srv, Config: cfg, Log: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ck := loginCookieOK(t, c)
	s := c.sessionByID(ck.Value)
	if s == nil {
		t.Fatal("no session for login cookie")
	}
	return &consoleEnv{c: c, srv: srv, ck: ck, s: s, lb: lb}
}

func (e *consoleEnv) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-CSRF-Token", e.s.csrf)
	r.Header.Set("HX-Request", "true")
	r.AddCookie(e.ck)
	rec := httptest.NewRecorder()
	e.c.Handler().ServeHTTP(rec, r)
	return rec
}

func (e *consoleEnv) exec(t *testing.T, line string) string {
	t.Helper()
	rec := e.post(t, "/console/exec", url.Values{"cmd": {line}})
	if rec.Code != http.StatusOK {
		t.Fatalf("exec %q: got status %d, want 200", line, rec.Code)
	}
	return rec.Body.String()
}

func TestConsole_ParseCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    []string
		wantErr bool
	}{
		{"plain", "set k v", []string{"set", "k", "v"}, false},
		{"double quoted space", `set k "a b"`, []string{"set", "k", "a b"}, false},
		{"single quoted", `echo 'x y'`, []string{"echo", "x y"}, false},
		{"hex escape", `"\x41"`, []string{"A"}, false},
		{"newline escape", `"a\nb"`, []string{"a\nb"}, false},
		{"single quote escape", `'it\'s'`, []string{"it's"}, false},
		{"adjacent quotes error", `'it''s'`, nil, true},
		{"unbalanced double", `set k "abc`, nil, true},
		{"unbalanced single", `set k 'abc`, nil, true},
		{"extra spaces", "  get   k  ", []string{"get", "k"}, false},
		{"empty", "   ", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommandLine(tc.line)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err %v, want error=%v", err, tc.wantErr)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConsole_ViewOf(t *testing.T) {
	bulk := func(s string) resp.Value { return resp.Value{Type: '$', Str: []byte(s)} }
	tests := []struct {
		name string
		in   resp.Value
		kind string
		text string
	}{
		{"status", resp.Value{Type: '+', Str: []byte("OK")}, "status", "OK"},
		{"error", resp.Value{Type: '-', Str: []byte("ERR boom")}, "error", "ERR boom"},
		{"int", resp.Value{Type: ':', Int: 7}, "int", "7"},
		{"bulk", bulk("hi"), "bulk", "hi"},
		{"nil bulk", resp.Value{Type: '$', Null: true}, "nil", "(nil)"},
		{"nil resp3", resp.Value{Type: '_', Null: true}, "nil", "(nil)"},
		{"double", resp.Value{Type: ',', Float: 1.5, Str: []byte("1.5")}, "double", "1.5"},
		{"bool", resp.Value{Type: '#', Int: 1}, "bool", "true"},
		{"verbatim", resp.Value{Type: '=', Str: []byte("txt:hello")}, "verbatim", "hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := viewOf(tc.in)
			if got.Kind != tc.kind || got.Text != tc.text {
				t.Fatalf("got %q/%q, want %q/%q", got.Kind, got.Text, tc.kind, tc.text)
			}
		})
	}
	m := viewOf(resp.Value{Type: '%', Elems: []resp.Value{bulk("a"), bulk("1")}})
	if m.Kind != "map" || len(m.Items) != 2 {
		t.Fatalf("map: got %+v", m)
	}
	a := viewOf(resp.Value{Type: '*', Elems: []resp.Value{bulk("x")}})
	if a.Kind != "array" || len(a.Items) != 1 || a.Items[0].Text != "x" {
		t.Fatalf("array: got %+v", a)
	}
}

func TestConsole_ExecRendersReplies(t *testing.T) {
	e := newConsoleEnv(t)
	e.exec(t, "HSET h f v")
	tests := []struct {
		line string
		want []string
	}{
		{"PING", []string{"reply-status", "PONG"}},
		{"INCR n", []string{"reply-int", ">1<"}},
		{"GET missing", []string{"reply-nil", "(nil)"}},
		{"HGETALL h", []string{"reply-map", ">f<", ">v<"}},
		{"NOSUCHCMD x", []string{"reply-error", "unknown command"}},
		{"set k", []string{"reply-error"}},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			out := e.exec(t, tc.line)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("fragment lacks %q: %s", w, out)
				}
			}
		})
	}
}

func TestConsole_ExecEscapesHTML(t *testing.T) {
	e := newConsoleEnv(t)
	out := e.exec(t, `SET x "<script>alert(1)</script>"`)
	out += e.exec(t, "GET x")
	if strings.Contains(out, "<script>") {
		t.Fatalf("unescaped markup in fragment: %s", out)
	}
}

func TestConsole_ParseErrorRendered(t *testing.T) {
	e := newConsoleEnv(t)
	out := e.exec(t, `get "abc`)
	if !strings.Contains(out, "reply-error") {
		t.Fatalf("got %s, want error fragment", out)
	}
}

func TestConsole_DeniedCommands(t *testing.T) {
	e := newConsoleEnv(t)
	denied := []string{
		"SUBSCRIBE x", "PSUBSCRIBE x*", "SSUBSCRIBE x", "MONITOR", "QUIT", "RESET", "client reply off",
		"BLPOP k 0", "BRPOP k 0", "BLMOVE a b LEFT RIGHT 0", "BRPOPLPUSH a b 0",
		"BZPOPMIN z 0", "BZPOPMAX z 0", "BLMPOP 0 1 k LEFT", "BZMPOP 0 1 z MIN",
		"XREAD BLOCK 0 STREAMS s $", "XREADGROUP GROUP g c BLOCK 0 STREAMS s >",
	}
	for _, line := range denied {
		t.Run(line, func(t *testing.T) {
			out := e.exec(t, line)
			if !strings.Contains(out, "reply-error") || !strings.Contains(out, "not allowed") {
				t.Fatalf("got %s, want refusal naming the reason", out)
			}
		})
	}
	if out := e.exec(t, "BLPOP k 1"); strings.Contains(out, "not allowed") {
		t.Fatalf("BLPOP k 1 refused: %s", out)
	}
	if out := e.exec(t, "XREAD BLOCK 10 STREAMS s $"); strings.Contains(out, "not allowed") {
		t.Fatalf("XREAD BLOCK 10 refused: %s", out)
	}
	// MONITOR must not have been executed: a normal command still replies normally.
	if out := e.exec(t, "PING"); !strings.Contains(out, "PONG") {
		t.Fatalf("PING after denied commands: %s", out)
	}
}

func TestConsole_HistoryPerSession(t *testing.T) {
	e := newConsoleEnv(t)
	for i := range 105 {
		e.exec(t, "ECHO "+strconv.Itoa(i))
	}
	e.s.mu.Lock()
	h := slices.Clone(e.s.history)
	e.s.mu.Unlock()
	if len(h) != 100 {
		t.Fatalf("got %d history entries, want 100", len(h))
	}
	if h[0] != "ECHO 104" || h[99] != "ECHO 5" {
		t.Fatalf("got first %q last %q, want newest first", h[0], h[99])
	}
	other := e.c.newSession("default")
	other.mu.Lock()
	n := len(other.history)
	other.mu.Unlock()
	if n != 0 {
		t.Fatalf("new session has %d history entries, want 0", n)
	}
}

func TestConsole_ConcurrentRequestsSameSession(t *testing.T) {
	e := newConsoleEnv(t)
	const workers, per = 8, 25
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				rec := e.post(t, "/console/exec", url.Values{"cmd": {"INCR ctr"}})
				if rec.Code != http.StatusOK {
					t.Errorf("got status %d", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
	out := e.exec(t, "GET ctr")
	if !strings.Contains(out, ">200<") {
		t.Fatalf("got %s, want counter 200", out)
	}
}

func TestConsole_AuditLogsCommandNameNotArgs(t *testing.T) {
	e := newConsoleEnv(t)
	e.exec(t, "SET secret hunter2")
	out := e.lb.String()
	if !strings.Contains(out, "web cmd") || !strings.Contains(out, "cmd=SET") || !strings.Contains(out, "keys=1") {
		t.Fatalf("log lacks audit line: %q", out)
	}
	if !strings.Contains(out, "user=") || !strings.Contains(out, "ip=") {
		t.Fatalf("audit line lacks user/ip: %q", out)
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "secret") {
		t.Fatalf("arguments leaked into log: %q", out)
	}
}

func TestConsole_BinaryRenderedAsHex(t *testing.T) {
	e := newConsoleEnv(t)
	e.exec(t, `SET b "\xff\x00"`)
	v, err := e.s.db.Exec("GET", "b")
	if err != nil {
		t.Fatal(err)
	}
	got := viewOf(v)
	if !got.Binary || got.Text != "ff 00" {
		t.Fatalf("got binary=%v text=%q, want true/%q", got.Binary, got.Text, "ff 00")
	}
	out := e.exec(t, "GET b")
	if !strings.Contains(out, "ff 00") || !strings.Contains(out, "reply-binary") {
		t.Fatalf("fragment: %s", out)
	}
}

func TestConsole_CompletePrefix(t *testing.T) {
	e := newConsoleEnv(t)
	get := func(q string) string {
		r := httptest.NewRequest(http.MethodGet, "/console/complete?q="+url.QueryEscape(q), nil)
		r.AddCookie(e.ck)
		rec := httptest.NewRecorder()
		e.c.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("complete %q: status %d", q, rec.Code)
		}
		return rec.Body.String()
	}
	out := get("hg")
	for _, w := range []string{"HGET", "HGETALL", `data-hint=`} {
		if !strings.Contains(out, w) {
			t.Errorf("q=hg lacks %q: %s", w, out)
		}
	}
	if strings.Contains(out, "SET</") && !strings.Contains(out, "HSET") {
		t.Errorf("unexpected match: %s", out)
	}
	if strings.Count(out, "data-cmd=") > 10 {
		t.Errorf("more than 10 suggestions: %s", out)
	}
	if out := strings.TrimSpace(get("")); out != "" {
		t.Errorf("q= got %q, want empty", out)
	}
	if out := get("hget k"); !strings.Contains(out, "HGET") {
		t.Errorf("q=hget k lacks hint: %s", out)
	}
}

func TestConsole_SamplesAreValidCommands(t *testing.T) {
	e := newConsoleEnv(t)
	if out := e.post(t, "/console/seed", nil).Body.String(); strings.Contains(out, "reply-error") {
		t.Fatalf("seed failed: %s", out)
	}
	if len(sampleGroups) < 10 {
		t.Fatalf("got %d sample groups, want >= 10", len(sampleGroups))
	}
	for _, g := range sampleGroups {
		if n := len(g.Examples); n < 3 || n > 6 {
			t.Errorf("group %s has %d examples, want 3-6", g.Name, n)
		}
		for _, ex := range g.Examples {
			t.Run(ex.Cmd, func(t *testing.T) {
				args, err := parseCommandLine(ex.Cmd)
				if err != nil || len(args) == 0 {
					t.Fatalf("parse: %v %q", err, args)
				}
				if ex.Desc == "" {
					t.Error("empty description")
				}
				if reason, bad := denyReason(args); bad {
					t.Fatalf("sample is denied: %s", reason)
				}
				info, err := e.s.db.Exec("COMMAND", "INFO", args[0])
				if err != nil || len(info.Elems) != 1 || info.Elems[0].Null {
					t.Fatalf("command %q not in table: %+v %v", args[0], info, err)
				}
				v, err := e.s.db.Exec(args...)
				if err != nil {
					t.Fatalf("exec: %v", err)
				}
				if v.Type == '-' {
					t.Fatalf("error reply: %s", v.Str)
				}
			})
		}
	}
}

func TestConsole_SeedRefusesWhenSamplesExist(t *testing.T) {
	e := newConsoleEnv(t)
	first := e.post(t, "/console/seed", nil).Body.String()
	if strings.Contains(first, "reply-error") {
		t.Fatalf("first seed: %s", first)
	}
	for _, typ := range []string{"string", "hash", "list", "set", "zset", "stream"} {
		v, err := e.s.db.Exec("EXISTS", "sample:"+typ)
		if err != nil || v.Int != 1 {
			t.Errorf("sample:%s missing after seed: %+v %v", typ, v, err)
		}
	}
	if _, err := e.s.db.Exec("SET", "sample:extra", "x"); err != nil {
		t.Fatal(err)
	}
	second := e.post(t, "/console/seed", nil).Body.String()
	if !strings.Contains(second, "reply-error") || !strings.Contains(second, "already") {
		t.Fatalf("second seed: %s, want refusal", second)
	}
	n, err := e.s.db.Exec("DBSIZE")
	if err != nil {
		t.Fatal(err)
	}
	if n.Int != 9 {
		t.Fatalf("got %d keys, want 9 (8 seeded + extra)", n.Int)
	}
}

func TestConsole_PageRendersSamplesAndScript(t *testing.T) {
	e := newConsoleEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/console", nil)
	r.AddCookie(e.ck)
	rec := httptest.NewRecorder()
	e.c.Handler().ServeHTTP(rec, r)
	body := rec.Body.String()
	for _, w := range []string{"/static/console.js", `hx-post="/console/exec"`, "data-cmd=", "Load sample data", "Strings", "Sorted sets"} {
		if !strings.Contains(body, w) {
			t.Errorf("console page lacks %q", w)
		}
	}
	if strings.Contains(body, " onclick=") || strings.Contains(body, "<script>") {
		t.Error("inline script found")
	}
	if g := get(t, e.c.Handler(), "/static/console.js"); g.Code != http.StatusOK {
		t.Fatalf("console.js: status %d", g.Code)
	}
}

func TestConsole_DeniedConnectionCommands(t *testing.T) {
	e := newConsoleEnv(t)
	for _, line := range []string{
		"HELLO 2", "AUTH x", "client tracking on", "SYNC", "PSYNC ? -1", "REPLCONF listening-port 1",
		"SHUTDOWN NOSAVE", "DEBUG SLEEP 0",
	} {
		t.Run(line, func(t *testing.T) {
			out := e.exec(t, line)
			if !strings.Contains(out, "not allowed") {
				t.Fatalf("got %s, want refusal", out)
			}
		})
	}
	if out := e.exec(t, "SAVE"); strings.Contains(out, "not allowed") {
		t.Fatalf("SAVE refused: %s", out)
	}
}

func TestConsole_XReadGroupNamesDoNotBypassDenyList(t *testing.T) {
	e := newConsoleEnv(t)
	for _, line := range []string{
		"XREADGROUP GROUP g block BLOCK 0 STREAMS s >",
		"XREADGROUP GROUP streams c BLOCK 0 STREAMS s >",
		"XREAD COUNT 1 BLOCK 0 STREAMS s $",
	} {
		if out := e.exec(t, line); !strings.Contains(out, "not allowed") {
			t.Errorf("%q: got %s, want refusal", line, out)
		}
	}
}

func TestConsole_BlockingTimeoutCapped(t *testing.T) {
	e := newConsoleEnv(t)
	for _, line := range []string{
		"BLPOP k 31", "BRPOP k 3600", "BLMPOP 1000 1 k LEFT", "XREAD BLOCK 30001 STREAMS s $",
		"XREADGROUP GROUP g c BLOCK 99999 STREAMS s >",
	} {
		if out := e.exec(t, line); !strings.Contains(out, "30 seconds") {
			t.Errorf("%q: got %s, want cap message", line, out)
		}
	}
	for _, line := range []string{"BLPOP k 0.01", "XREAD BLOCK 10 STREAMS s $"} {
		if out := e.exec(t, line); strings.Contains(out, "not allowed") {
			t.Errorf("%q refused: %s", line, out)
		}
	}
}

func TestConsole_MultiExecAlignedAndIndicated(t *testing.T) {
	e := newConsoleEnv(t)
	e.exec(t, "MULTI")
	out := e.exec(t, "SET a 1")
	if !strings.Contains(out, "QUEUED") || !strings.Contains(out, "1 queued") {
		t.Fatalf("SET in MULTI: %s", out)
	}
	out = e.exec(t, "INCR a")
	if !strings.Contains(out, "2 queued") {
		t.Fatalf("INCR in MULTI: %s", out)
	}
	if seed := e.post(t, "/console/seed", nil).Body.String(); !strings.Contains(seed, "transaction is open") {
		t.Fatalf("seed in MULTI: %s", seed)
	}
	out = e.exec(t, "EXEC")
	if strings.Count(out, "<li>") != 2 || !strings.Contains(out, ">OK<") || !strings.Contains(out, ">2<") {
		t.Fatalf("EXEC reply misaligned: %s", out)
	}
	if strings.Contains(out, "transaction open") {
		t.Fatalf("indicator still shown after EXEC: %s", out)
	}
	e.exec(t, "MULTI")
	if out := e.exec(t, "DISCARD"); strings.Contains(out, "transaction open") {
		t.Fatalf("indicator after DISCARD: %s", out)
	}
}

func TestConsole_HistoryRedactsSecrets(t *testing.T) {
	e := newConsoleEnv(t)
	outs := []string{
		e.exec(t, "AUTH hunter2"),
		e.exec(t, "HELLO 3 AUTH default hunter2"),
		e.exec(t, "CONFIG SET masterauth hunter2"),
		e.exec(t, "config set maxmemory-policy noeviction requirepass hunter2"),
	}
	for _, o := range outs {
		if strings.Contains(o, "hunter2") {
			t.Errorf("secret rendered: %s", o)
		}
	}
	for _, h := range e.s.historyCopy() {
		if strings.Contains(h, "hunter2") {
			t.Errorf("secret in history: %q", h)
		}
	}
	if !strings.Contains(strings.Join(e.s.historyCopy(), "|"), "CONFIG SET ...(redacted)") {
		t.Errorf("history lacks redacted entry: %q", e.s.historyCopy())
	}
}

func TestConsole_DeniedLinesNotRecordedClientSide(t *testing.T) {
	e := newConsoleEnv(t)
	if out := e.exec(t, "PING"); !strings.Contains(out, `data-history="PING"`) {
		t.Errorf("executed command lacks data-history: %s", out)
	}
	for _, line := range []string{"MONITOR", `get "x`} {
		if out := e.exec(t, line); strings.Contains(out, "data-history") {
			t.Errorf("%q marked for history: %s", line, out)
		}
	}
}

func TestConsole_AuditUnknownCommandName(t *testing.T) {
	e := newConsoleEnv(t)
	e.exec(t, "hunter2 more")
	out := e.lb.String()
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "cmd=unknown") {
		t.Fatalf("log: %q", out)
	}
}

func TestConsole_XReadLastBlockWins(t *testing.T) {
	e := newConsoleEnv(t)
	for _, line := range []string{
		"XREAD BLOCK 1 BLOCK 0 STREAMS s $",
		"XREAD BLOCK 1 BLOCK 999999 STREAMS s $",
	} {
		if out := e.exec(t, line); !strings.Contains(out, "not allowed") {
			t.Errorf("%q: got %s, want refusal", line, out)
		}
	}
	if out := e.exec(t, "XREAD BLOCK 999999 BLOCK 1 STREAMS s $"); strings.Contains(out, "not allowed") {
		t.Errorf("last BLOCK 1 refused: %s", out)
	}
}

func TestConsole_RedactsACLAndMigrate(t *testing.T) {
	e := newConsoleEnv(t)
	for _, line := range []string{
		"ACL SETUSER bob on >hunter2 +@all",
		"ACL SETUSER bob #5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
		"MIGRATE 127.0.0.1 1 k 0 10 AUTH hunter2",
		"MIGRATE 127.0.0.1 1 k 0 10 AUTH2 u hunter2",
	} {
		out := e.exec(t, line)
		if strings.Contains(out, "hunter2") || strings.Contains(out, "5e8848") {
			t.Errorf("%q: secret rendered: %s", line, out)
		}
	}
	for _, h := range e.s.historyCopy() {
		if strings.Contains(h, "hunter2") || strings.Contains(h, "5e8848") {
			t.Errorf("secret in history: %q", h)
		}
	}
}
