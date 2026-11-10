package web

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
)

func TestDashboard_SetMaxMemory(t *testing.T) {
	e := newConsoleEnv(t)
	rec := e.post(t, "/settings/memory", url.Values{"maxmemory": {"4gb"}, "policy": {"allkeys-lru"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if got := e.exec(t, "CONFIG GET maxmemory"); !strings.Contains(got, "4294967296") {
		t.Fatalf("got %s, want maxmemory 4294967296", got)
	}
	if got := e.exec(t, "CONFIG GET maxmemory-policy"); !strings.Contains(got, "allkeys-lru") {
		t.Fatalf("got %s, want policy allkeys-lru", got)
	}
	out := e.lb.String()
	if !strings.Contains(out, "setting=maxmemory") || !strings.Contains(out, "value=4gb") {
		t.Fatalf("log lacks audit line: %q", out)
	}
}

func TestDashboard_SetMaxMemoryRejects(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
	}{
		{"bad size", url.Values{"maxmemory": {"4zb"}, "policy": {"allkeys-lru"}}},
		{"bad policy", url.Values{"maxmemory": {"4gb"}, "policy": {"bogus"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newConsoleEnv(t)
			rec := e.post(t, "/settings/memory", tc.form)
			if !strings.Contains(rec.Body.String(), `class="form-error"`) {
				t.Fatalf("got %s, want inline error", rec.Body.String())
			}
			if got := e.exec(t, "CONFIG GET maxmemory"); !strings.Contains(got, ">0<") {
				t.Fatalf("got %s, want maxmemory unchanged at 0", got)
			}
		})
	}
}

func TestDashboard_SetMaxMemoryNeedsCSRF(t *testing.T) {
	e := newConsoleEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/settings/memory", strings.NewReader("maxmemory=4gb&policy=allkeys-lru"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(e.ck)
	rec := httptest.NewRecorder()
	e.c.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rec.Code)
	}
}

func snapAt(i int) server.StatsSnapshot { return server.StatsSnapshot{Commands: int64(i)} }

func TestDashboard_SamplerRing(t *testing.T) {
	s := newSampler()
	for i := range 305 {
		s.add(snapAt(i), latency{})
	}
	got := s.last(1000)
	if len(got) != ringSize {
		t.Fatalf("got %d samples, want %d", len(got), ringSize)
	}
	for i, g := range got {
		if g.Commands != int64(i+5) {
			t.Fatalf("sample %d: got %d, want %d", i, g.Commands, i+5)
		}
	}
	if tail := s.last(2); len(tail) != 2 || tail[1].Commands != 304 {
		t.Fatalf("got %v, want last two ending at 304", tail)
	}
}

func TestDashboard_SamplerRunTicks(t *testing.T) {
	s := newSampler()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	finished := make(chan struct{})
	n := 0
	go func() {
		defer close(finished)
		s.run(ticks, done, func() (server.StatsSnapshot, latency) { n++; return snapAt(n), latency{} })
	}()
	ticks <- time.Time{}
	ticks <- time.Time{}
	close(done)
	<-finished
	if got := s.last(10); len(got) != 3 { // one immediate sample plus two ticks
		t.Fatalf("got %d samples, want 3", len(got))
	}
}

func TestDashboard_HitRateZeroDivision(t *testing.T) {
	if got := hitRate(0, 0); got != "—" {
		t.Fatalf("got %q, want em dash", got)
	}
	if got := hitRate(3, 1); got != "75.0%" {
		t.Fatalf("got %q, want 75.0%%", got)
	}
}

func TestDashboard_ParseLatency(t *testing.T) {
	in := "# Latencystats\r\nlatency_percentiles_usec_get:p50=1.003,p99=5.023,p99.9=9.0\r\nlatency_percentiles_usec_set:p50=2.007,p99=3.007\r\n"
	got := parseLatency(in)
	if got.P50 != "2.0 µs" || got.P99 != "5.0 µs" {
		t.Fatalf("got %+v, want p50 2.0 µs p99 5.0 µs", got)
	}
	if got := parseLatency("# Latencystats\r\n"); got.P50 != "" || got.P99 != "" {
		t.Fatalf("got %+v, want empty", got)
	}
}

func TestDashboard_PageRendersTiles(t *testing.T) {
	e := newConsoleEnv(t)
	rec := authedGet(e.c, "/", e.ck, nil)
	body := rec.Body.String()
	for _, want := range []string{"ops/s", "p50", "p99", "hit rate", `sse-connect="/events/stats"`, `sse-swap="tiles"`, `name="maxmemory"`, `value="volatile-ttl"`, "—"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestDashboard_HeatmapHasCellPerShard(t *testing.T) {
	cfg := config.Default()
	cfg.Shards = 16
	if err := cfg.Set("requirepass", testPass, true); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Server: server.New(cfg, nil), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ck := loginCookieOK(t, c)
	body := authedGet(c, "/", ck, nil).Body.String()
	if got := strings.Count(body, `<rect class="shard"`); got != 16 {
		t.Fatalf("got %d shard cells, want 16", got)
	}
}

func TestDashboard_RequiresSession(t *testing.T) {
	e := newConsoleEnv(t)
	for _, p := range []string{"/", "/events/stats"} {
		rec := authedGet(e.c, p, nil, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("%s: got %d %q, want redirect to /login", p, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestDashboard_SSEEmitsTiles(t *testing.T) {
	e := newConsoleEnv(t)
	ts := httptest.NewServer(e.c.Handler())
	t.Cleanup(ts.Close)
	base := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/stats", nil)
	req.AddCookie(e.ck)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("got content type %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var event, data bool
	for sc.Scan() {
		line := sc.Text()
		event = event || line == "event: tiles"
		if event && strings.HasPrefix(line, "data: ") && strings.Contains(line, "ops/s") {
			data = true
			break
		}
	}
	if !data {
		t.Fatalf("no tiles frame with ops/s: %v", sc.Err())
	}
	_ = resp.Body.Close() // intentional disconnect
	http.DefaultClient.CloseIdleConnections()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > base {
		t.Fatalf("got %d goroutines, want at most %d after disconnect", got, base)
	}
}

// openStream connects to /events/stats with the env's session.
func openStream(t *testing.T, base string, e *consoleEnv) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/events/stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(e.ck)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDashboard_StreamCapPerSession(t *testing.T) {
	e := newConsoleEnv(t)
	ts := httptest.NewServer(e.c.Handler())
	t.Cleanup(ts.Close)
	for i := range maxStreamsPerSession {
		if resp := openStream(t, ts.URL, e); resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: got status %d, want 200", i, resp.StatusCode)
		}
	}
	if resp := openStream(t, ts.URL, e); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", resp.StatusCode)
	}
}

func TestDashboard_StreamKeepalive(t *testing.T) {
	e := newConsoleEnv(t)
	e.c.keepalive = 20 * time.Millisecond
	ts := httptest.NewServer(e.c.Handler())
	t.Cleanup(ts.Close)
	sc := bufio.NewScanner(openStream(t, ts.URL, e).Body)
	for sc.Scan() {
		if sc.Text() == ": keepalive" {
			return
		}
	}
	t.Fatalf("no keepalive comment: %v", sc.Err())
}

func TestDashboard_StreamEndsWithSession(t *testing.T) {
	e := newConsoleEnv(t)
	e.c.keepalive = 20 * time.Millisecond
	ts := httptest.NewServer(e.c.Handler())
	t.Cleanup(ts.Close)
	resp := openStream(t, ts.URL, e)
	e.c.authMu.Lock()
	delete(e.c.sessions, e.s.id)
	e.c.authMu.Unlock()
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(resp.Body).WriteTo(discard{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after its session was removed")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestDashboard_StreamWriteDeadline(t *testing.T) {
	e := newConsoleEnv(t)
	e.c.writeTimeout = 100 * time.Millisecond
	ts := httptest.NewServer(e.c.Handler())
	t.Cleanup(ts.Close)
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(1024) // keep the stalled receiver's window small
	}
	fmt.Fprintf(conn, "GET /events/stats HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\n\r\n", e.ck.Name, e.ck.Value)
	waitFor(t, "stream to open", func() bool { return e.s.streams.Load() == 1 })
	// Never read; keep producing samples until the stalled write times out.
	snap := e.srv.Stats()
	waitFor(t, "handler to give up on the stalled client", func() bool {
		e.c.sampler.add(snap, latency{})
		return e.s.streams.Load() == 0
	})
}

func TestDashboard_AuditsRejectedMemory(t *testing.T) {
	e := newConsoleEnv(t)
	e.post(t, "/settings/memory", url.Values{"maxmemory": {"4zb"}, "policy": {"allkeys-lru"}})
	if out := e.lb.String(); !strings.Contains(out, "setting=maxmemory") || !strings.Contains(out, "result=error") {
		t.Fatalf("got %q, want an audit line with result=error", out)
	}
}

func TestDashboard_Accessibility(t *testing.T) {
	e := newConsoleEnv(t)
	body := authedGet(e.c, "/", e.ck, nil).Body.String()
	if !strings.Contains(body, `aria-label="not available"`) {
		t.Error("dash lacks accessible text")
	}
	if !strings.Contains(body, `class="sr-only"`) {
		t.Error("heatmap lacks a text table")
	}
	e.exec(t, "CONFIG SET maxmemory 1gb")
	e.c.sampler.add(e.srv.Stats(), latency{})
	body = authedGet(e.c, "/", e.ck, nil).Body.String()
	for _, want := range []string{`role="meter"`, `aria-valuenow=`, `aria-valuemax="100"`} {
		if !strings.Contains(body, want) {
			t.Errorf("meter lacks %s", want)
		}
	}
	if strings.Contains(body, `fill="#`) {
		t.Error("heatmap cells hardcode a fill colour")
	}
}

func TestDashboard_HeatTopShards(t *testing.T) {
	cur := server.StatsSnapshot{Shards: make([]server.ShardStat, 32)}
	prev := server.StatsSnapshot{Shards: make([]server.ShardStat, 32)}
	cur.Shards[7].Ops = 50
	cur.Shards[3].Ops = 90
	h := buildHeat(cur, &prev)
	if len(h.Top) != topShards || h.Top[0].Shard != 3 || h.Top[1].Shard != 7 {
		t.Fatalf("got top %+v, want shards 3 then 7, %d rows", h.Top[:2], topShards)
	}
}
