// This file implements the dashboard: a once-per-second statistics sampler
// with a bounded history ring, the view model behind the live tiles,
// sparklines and shard heatmap, the SSE stream that pushes the rendered tiles
// to the browser, and the maxmemory/policy control.
package web

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rabbicse/distcache/internal/server"
)

const (
	// ringSize is how many samples the sampler keeps (five minutes at 1 Hz).
	ringSize = 300
	// sampleEvery is the sampling period.
	sampleEvery = time.Second
	// keepaliveEvery is the SSE comment interval that keeps proxies from
	// closing an idle stream.
	keepaliveEvery = 15 * time.Second
	// streamWriteTimeout bounds each SSE write+flush so a client that stops
	// reading cannot pin its handler goroutine and session.
	streamWriteTimeout = 10 * time.Second
	// maxStreamsPerSession caps open dashboard streams per browser session.
	maxStreamsPerSession = 4
	// idleOpacity is the faintest heatmap cell; it keeps 3:1 contrast with
	// the card surface so idle shards stay visible.
	idleOpacity = 0.25

	sparkW, sparkH = 240, 36 // sparkline viewBox
	heatCell       = 14      // heatmap cell edge, in viewBox units
	heatGap        = 2
	meterW         = 160 // memory meter viewBox width
	noValue        = "—"
	maxSizeArg     = 32 // longest accepted maxmemory string
	topShards      = 10 // rows in the screen-reader shard table
)

// evictionPolicies are the values CONFIG SET maxmemory-policy accepts; the
// control validates against them before sending anything to the server.
var evictionPolicies = []string{
	"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random",
	"volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl",
}

// latency is the command latency summary shown on the tiles; empty fields
// mean the server does not report latency statistics.
type latency struct{ P50, P99 string }

// sampler keeps the last ringSize statistics snapshots. One goroutine per
// Console feeds it; any number of SSE handlers read it.
type sampler struct {
	mu      sync.Mutex // guards every field below
	ring    []server.StatsSnapshot
	lat     latency
	changed chan struct{}                                // closed and replaced on every add
	frame   []byte                                       // rendered SSE event for the newest sample; nil without render
	render  func([]server.StatsSnapshot, latency) []byte // set before the goroutine starts; may be nil
	stopped bool
	stop    chan struct{} // closed by shutdown; unblocks waiters
}

func newSampler() *sampler {
	return &sampler{changed: make(chan struct{}), stop: make(chan struct{})}
}

// add appends a snapshot, dropping the oldest beyond ringSize, and wakes
// everyone waiting for the next sample.
func (s *sampler) add(snap server.StatsSnapshot, lat latency) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) == ringSize {
		s.ring = append(s.ring[:0], s.ring[1:]...)
	}
	s.ring = append(s.ring, snap)
	s.lat = lat
	if s.render != nil {
		// Rendered once per sample here, then fanned out to every stream.
		s.frame = s.render(s.ring, lat)
	}
	close(s.changed)
	s.changed = make(chan struct{})
}

// last returns up to n of the newest samples, oldest first.
func (s *sampler) last(n int) []server.StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ring[max(0, len(s.ring)-n):])
}

// latest returns the latency summary of the newest sample.
func (s *sampler) latest() latency {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lat
}

// next returns a channel closed when a sample newer than the current one
// arrives, or when the sampler shuts down.
func (s *sampler) next() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// currentFrame returns the SSE event bytes for the newest sample, or nil.
func (s *sampler) currentFrame() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame
}

// shutdown releases waiters and SSE handlers; it is idempotent.
func (s *sampler) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
}

// run samples once immediately, then on every tick until done closes. The
// immediate sample means a freshly opened dashboard never starts empty.
func (s *sampler) run(ticks <-chan time.Time, done <-chan struct{}, src func() (server.StatsSnapshot, latency)) {
	s.add(src())
	for {
		select {
		case <-ticks:
			s.add(src())
		case <-done:
			return
		}
	}
}

// startSampler launches the sampler goroutine. It is owned by Close, which
// calls stopSampler. Without a server there is nothing to sample.
func (c *Console) startSampler() {
	if c.srv == nil {
		return
	}
	s := newSampler()
	s.render = c.renderFrame
	db := c.srv.NewSession() // dedicated: INFO must not contend with browser sessions
	ticks, stopTick := c.sampleTick()
	done, finished := make(chan struct{}), make(chan struct{})
	src := func() (server.StatsSnapshot, latency) {
		return c.srv.Stats(), queryLatency(db)
	}
	go func() {
		defer close(finished)
		defer stopTick()
		s.run(ticks, done, src)
	}()
	c.sampler = s
	c.stopSampler = func() {
		close(done)
		s.shutdown()
		<-finished
		db.Close()
	}
}

// queryLatency reads INFO latencystats; any failure yields an empty summary.
func queryLatency(db *server.Session) latency {
	v, err := db.Exec("INFO", "latencystats")
	if err != nil || (v.Type != '$' && v.Type != '=') {
		return latency{}
	}
	return parseLatency(string(v.Str))
}

// parseLatency summarises latency_percentiles_usec_* lines as the worst p50
// and p99 across commands, the figure an operator watches.
func parseLatency(info string) latency {
	var p50, p99 float64
	found := false
	for line := range strings.SplitSeq(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "latency_percentiles_usec_") {
			continue
		}
		_, fields, _ := strings.Cut(line, ":")
		for f := range strings.SplitSeq(fields, ",") {
			k, v, _ := strings.Cut(f, "=")
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			switch k {
			case "p50":
				p50, found = max(p50, x), true
			case "p99":
				p99, found = max(p99, x), true
			}
		}
	}
	if !found {
		return latency{}
	}
	return latency{P50: fmtMicros(p50), P99: fmtMicros(p99)}
}

func fmtMicros(us float64) string {
	if us >= 1000 {
		return fmt.Sprintf("%.1f ms", us/1000)
	}
	return fmt.Sprintf("%.1f µs", us)
}

// hitRate formats hits/(hits+misses), or a dash when nothing was looked up.
func hitRate(hits, misses int64) string {
	if hits+misses == 0 {
		return noValue
	}
	return fmt.Sprintf("%.1f%%", 100*float64(hits)/float64(hits+misses))
}

// humanBytes formats n with binary units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, suffix := float64(n), "KMGTPE"
	i := -1
	for ; f >= unit && i < len(suffix)-1; i++ {
		f /= unit
	}
	return fmt.Sprintf("%.1f %ciB", f, suffix[i])
}

// sparkView is one sparkline: polyline points in a sparkW x sparkH box.
type sparkView struct {
	Label  string
	Points string
	Max    string
	W, H   int
}

// cell is one heatmap rectangle.
type cell struct {
	X, Y    int
	Opacity string
	Waited  bool // the shard waited on its lock in the last interval
	Title   string
}

// topShard is one row of the screen-reader table.
type topShard struct {
	Shard, Keys int
	Ops         int64
	Waited      bool
}

type heatView struct {
	W, H, Size int
	Cells      []cell
	Top        []topShard // busiest shards first, for the text alternative
}

// stat is a tile value; templates mark the "not available" dash for screen
// readers through Dash.
type stat string

// Dash reports whether the value is the not-available placeholder.
func (v stat) Dash() bool { return v == noValue }

// tilesView is the data for tiles.html.
type tilesView struct {
	OpsPerSec, P50, P99                stat
	MemUsed                            stat
	MemMax, MemPct                     string
	MeterW, MeterFill, MemPctInt       int
	Clients, HitRate, Evicted, Expired stat
	Role                               stat
	Sparks                             []sparkView
	Heat                               heatView
}

// buildTiles derives the tiles from the sampler history; with no samples it
// returns placeholders.
func buildTiles(samples []server.StatsSnapshot, lat latency) tilesView {
	t := tilesView{OpsPerSec: noValue, P50: stat(orDash(lat.P50)), P99: stat(orDash(lat.P99)),
		MemUsed: noValue, MemMax: noValue, Clients: noValue, HitRate: noValue,
		Evicted: noValue, Expired: noValue, Role: noValue, MeterW: meterW}
	if len(samples) == 0 {
		return t
	}
	cur := samples[len(samples)-1]
	t.OpsPerSec = stat(strconv.FormatInt(cur.OpsPerSec, 10))
	t.MemUsed = stat(humanBytes(cur.UsedMemory))
	t.MemMax = "unlimited"
	if cur.MaxMemory > 0 {
		t.MemMax = humanBytes(cur.MaxMemory)
		frac := math.Min(1, float64(cur.UsedMemory)/float64(cur.MaxMemory))
		t.MemPct = fmt.Sprintf("%.0f%%", 100*frac)
		t.MeterFill = int(frac * meterW)
		t.MemPctInt = int(math.Round(100 * frac))
	}
	t.Clients = stat(strconv.FormatInt(cur.Clients, 10))
	t.HitRate = stat(hitRate(cur.Hits, cur.Misses))
	t.Evicted = stat(strconv.FormatInt(cur.Evicted, 10))
	t.Expired = stat(strconv.FormatInt(cur.Expired, 10))
	t.Role = stat(cur.Role)
	t.Sparks = []sparkView{
		spark("ops/s", samples, func(s server.StatsSnapshot) float64 { return float64(s.OpsPerSec) }, func(v float64) string { return strconv.FormatInt(int64(v), 10) }),
		spark("memory", samples, func(s server.StatsSnapshot) float64 { return float64(s.UsedMemory) }, func(v float64) string { return humanBytes(int64(v)) }),
		spark("clients", samples, func(s server.StatsSnapshot) float64 { return float64(s.Clients) }, func(v float64) string { return strconv.FormatInt(int64(v), 10) }),
	}
	var prev *server.StatsSnapshot
	if len(samples) > 1 {
		prev = &samples[len(samples)-2]
	}
	t.Heat = buildHeat(cur, prev)
	return t
}

func orDash(s string) string {
	if s == "" {
		return noValue
	}
	return s
}

// spark builds a sparkline over samples, scaled from zero to the series max.
func spark(label string, samples []server.StatsSnapshot, val func(server.StatsSnapshot) float64, format func(float64) string) sparkView {
	hi := 0.0
	for _, s := range samples {
		hi = max(hi, val(s))
	}
	var b strings.Builder
	if len(samples) > 1 {
		for i, s := range samples {
			x := float64(i) * sparkW / float64(ringSize-1)
			y := float64(sparkH - 2) // 2px headroom so the stroke is not clipped
			if hi > 0 {
				y = float64(sparkH-2) - val(s)/hi*float64(sparkH-4)
			}
			fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
		}
	}
	return sparkView{Label: label, Points: strings.TrimSpace(b.String()), Max: format(hi), W: sparkW, H: sparkH}
}

// buildHeat builds the shard grid. Intensity is the ops each shard served
// since the previous sample; the outline marks shards that spent time waiting
// for their lock in that interval (a cumulative total would stay lit forever).
func buildHeat(cur server.StatsSnapshot, prev *server.StatsSnapshot) heatView {
	n := len(cur.Shards)
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	rows := 0
	if cols > 0 {
		rows = (n + cols - 1) / cols
	}
	step := heatCell + heatGap
	h := heatView{W: cols * step, H: rows * step, Size: heatCell}
	ops := make([]int64, n)
	waited := make([]bool, n)
	var peak int64
	for i, sh := range cur.Shards {
		if prev != nil && i < len(prev.Shards) {
			ops[i] = max(0, sh.Ops-prev.Shards[i].Ops)
			waited[i] = sh.LockWait > prev.Shards[i].LockWait
		}
		peak = max(peak, ops[i])
	}
	for i := range n {
		op := idleOpacity // idle shards stay visible
		if peak > 0 {
			op += (1 - idleOpacity) * float64(ops[i]) / float64(peak)
		}
		c := cell{X: i % cols * step, Y: i / cols * step, Opacity: strconv.FormatFloat(op, 'f', 2, 64),
			Title: fmt.Sprintf("Shard %d: %d ops, %d keys", i, ops[i], cur.Shards[i].Keys)}
		if waited[i] {
			c.Waited = true
			c.Title += ", waited on lock"
		}
		h.Cells = append(h.Cells, c)
		h.Top = append(h.Top, topShard{Shard: i, Keys: int(cur.Shards[i].Keys), Ops: ops[i], Waited: waited[i]})
	}
	slices.SortStableFunc(h.Top, func(a, b topShard) int { return cmp.Compare(b.Ops, a.Ops) })
	h.Top = h.Top[:min(len(h.Top), topShards)]
	return h
}

// currentTiles builds the tiles from the sampler, tolerating no sampler.
func (c *Console) currentTiles() tilesView {
	if c.sampler == nil {
		return buildTiles(nil, latency{})
	}
	return buildTiles(c.sampler.last(ringSize), c.sampler.latest())
}

// memoryView is the data for the memory-control fragment.
type memoryView struct {
	MaxMemory string
	Policy    string
	Policies  []string
	Error     string
	OK        bool
}

// memoryControl reads the current maxmemory and policy through the session.
func (c *Console) memoryControl(s *session) memoryView {
	v := memoryView{MaxMemory: "0", Policy: "noeviction", Policies: evictionPolicies}
	if s.db == nil {
		return v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := configValue(s.db, "maxmemory"); m != "" {
		v.MaxMemory = m
	}
	if p := configValue(s.db, "maxmemory-policy"); p != "" {
		v.Policy = p
	}
	return v
}

// configValue returns one CONFIG GET value, or "" on any failure. The caller
// holds the session lock.
func configValue(db *server.Session, name string) string {
	r, err := db.Exec("CONFIG", "GET", name)
	if err != nil {
		return ""
	}
	view := viewOf(r)
	switch {
	case view.Kind == "map" && len(view.Items) >= 2:
		return view.Items[1].Text
	case view.Kind == "array" && len(view.Items) >= 2:
		return view.Items[1].Text
	}
	return ""
}

// handleDashboard renders the dashboard page.
func (c *Console) handleDashboard(w http.ResponseWriter, r *http.Request) {
	p := page{Title: "Dashboard", Active: "dashboard", Insecure: c.insecure()}
	if s := sessionOf(r); s != nil {
		p.CSRF, p.HXHeaders = s.csrf, hxHeaders(s.csrf)
		mv := c.memoryControl(s)
		p.Memory = &mv
	}
	p.Tiles = c.currentTiles()
	c.render(w, http.StatusOK, "layout.html", p)
}

// handleMemory applies maxmemory and maxmemory-policy from the form. The
// policy is validated here; the size is validated by the server, whose error
// reply is shown inline.
func (c *Console) handleMemory(w http.ResponseWriter, r *http.Request) {
	s := sessionOf(r)
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		c.writeMemory(w, s, "request too large")
		return
	}
	size := strings.TrimSpace(r.PostFormValue("maxmemory"))
	policy := r.PostFormValue("policy")
	switch {
	case s.db == nil:
		c.writeMemory(w, s, "console has no server attached")
	case size == "" || len(size) > maxSizeArg || strings.ContainsAny(size, " \t\r\n"):
		c.writeMemory(w, s, "maxmemory must be a size such as 512mb, 4gb or 0")
	case !slices.Contains(evictionPolicies, policy):
		c.writeMemory(w, s, "unknown eviction policy")
	default:
		c.writeMemory(w, s, c.applyMemory(r, s, size, policy))
	}
}

// applyMemory runs both CONFIG SET commands, maxmemory first so an invalid
// size leaves the policy untouched. It returns an error message or "". Every
// attempt, rejected or not, is audited.
func (c *Console) applyMemory(r *http.Request, s *session, size, policy string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, kv := range [][2]string{{"maxmemory", size}, {"maxmemory-policy", policy}} {
		msg := c.setConfig(r, s, kv[0], kv[1])
		if msg == "" {
			continue
		}
		if i > 0 {
			return fmt.Sprintf("maxmemory set to %s; policy not changed: %s", size, msg)
		}
		return msg
	}
	return ""
}

// setConfig runs one CONFIG SET and audits the outcome. The caller holds the
// session lock.
func (c *Console) setConfig(r *http.Request, s *session, name, value string) string {
	ip := clientIP(r, c.trusted).String()
	v, err := s.db.Exec("CONFIG", "SET", name, value)
	if err != nil {
		c.log.Warn("web setting", "user", s.user, "ip", ip, "setting", name, "value", value, "result", "error", "err", err)
		return "internal error applying the setting"
	}
	if view := viewOf(v); view.Kind == "error" {
		c.log.Info("web setting", "user", s.user, "ip", ip, "setting", name, "value", value, "result", "error")
		return view.Text
	}
	c.log.Info("web setting", "user", s.user, "ip", ip, "setting", name, "value", value, "result", "ok")
	return ""
}

func (c *Console) writeMemory(w http.ResponseWriter, s *session, errMsg string) {
	v := c.memoryControl(s)
	v.Error, v.OK = errMsg, errMsg == ""
	c.renderFragment(w, "memory", v)
}

// renderFrame renders the SSE tiles event for samples; it runs in the
// sampler once per sample. It returns nil if the template fails.
func (c *Console) renderFrame(samples []server.StatsSnapshot, lat latency) []byte {
	var buf bytes.Buffer
	if err := c.tmpl.ExecuteTemplate(&buf, "tiles", buildTiles(samples, lat)); err != nil {
		c.log.Error("render tiles", "err", err)
		return nil
	}
	var out bytes.Buffer
	out.WriteString("event: tiles\n")
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		out.WriteString("data: " + line + "\n")
	}
	out.WriteString("\n")
	return out.Bytes()
}

// sessionLive reports whether s is still the registered, unexpired session.
// Unlike sessionByID it does not refresh the idle timer, so an open stream
// does not keep a session alive.
func (c *Console) sessionLive(s *session) bool {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.sessions[s.id] == s && !c.expired(s, c.now())
}

// streamWrite writes b and flushes under a write deadline, then clears the
// deadline. It reports whether the client is still reachable.
func (c *Console) streamWrite(w http.ResponseWriter, rc *http.ResponseController, b []byte) bool {
	// Unsupported deadlines (some test recorders) are not fatal.
	_ = rc.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	_, err := w.Write(b)
	if err == nil {
		err = rc.Flush()
	}
	_ = rc.SetWriteDeadline(time.Time{})
	return err == nil
}

// handleStatsStream is the SSE endpoint: one "tiles" event per sample, a
// keepalive comment while idle, and a return when the client leaves, its
// session ends or the sampler stops. Streams are capped per session.
func (c *Console) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	s := sessionOf(r)
	if c.sampler == nil {
		http.Error(w, "statistics unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.streams.Add(1) > maxStreamsPerSession {
		s.streams.Add(-1)
		http.Error(w, "too many open dashboards for this session", http.StatusTooManyRequests)
		return
	}
	defer s.streams.Add(-1)
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if !c.streamWrite(w, rc, []byte(": connected\n\n")) {
		return
	}
	keepalive := time.NewTicker(c.keepalive)
	defer keepalive.Stop()
	wake := c.sampler.next()
	for {
		if f := c.sampler.currentFrame(); f != nil && !c.streamWrite(w, rc, f) {
			return
		}
		for woke := false; !woke; {
			select {
			case <-r.Context().Done():
				return
			case <-c.sampler.stop:
				return
			case <-keepalive.C:
				if !c.sessionLive(s) || !c.streamWrite(w, rc, []byte(": keepalive\n\n")) {
					return
				}
			case <-wake:
				if !c.sessionLive(s) {
					return
				}
				wake, woke = c.sampler.next(), true
			}
		}
	}
}
