// This file implements console authentication: the login and logout
// handlers, server-side sessions, CSRF protection, per-IP login lockout and
// the janitor that expires sessions and stale lockout entries (spec §4).
//
// Login CSRF: there is no session before login, so the login POST is
// protected by (1) a pre-session token, set by GET /login as the
// SameSite=Strict HttpOnly cookie dc_login and echoed in the form, which a
// cross-site page can neither read nor make the browser send, and (2) an
// Origin check that rejects any POST whose Origin header names another host.
// Origin alone is not enough because the console's Referrer-Policy
// no-referrer makes browsers send "Origin: null" on same-origin form posts.
// A real Origin must match the expected scheme and host, taken from
// X-Forwarded-Proto / X-Forwarded-Host when the peer is a trusted proxy.
//
// Cookies: over HTTPS they carry the __Host- prefix (__Host-dc_session,
// __Host-dc_login), which forces Secure, Path=/ and no Domain so a sibling
// service on the same host cannot plant them; over HTTP plain names are used.
//
// Lockout: failures are keyed per IPv4 address or per IPv6 /64. The lock check
// and an in-flight attempt reservation happen in one critical section, so
// concurrent requests cannot exceed 5 password checks per window. In addition,
// more than 100 failures across all clients within 1 minute rejects every
// login attempt for 1 minute (logged once at warn).
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rabbicse/distcache/internal/server"
)

// ErrNoPassword is returned by New when the console would run without any
// authentication: requirepass is empty and web-insecure is off.
var ErrNoPassword = errors.New("web console requires requirepass (or web-insecure yes)")

const (
	sessionCookie = "dc_session"
	loginCookie   = "dc_login"

	absoluteSessionLifetime = 12 * time.Hour

	maxLoginFailures = 5
	lockoutWindow    = 5 * time.Minute // failures counted within this window
	lockoutDuration  = 5 * time.Minute

	// maxLockoutEntries bounds the lockout map so a client spraying source
	// addresses cannot grow it without limit; the oldest entry is evicted.
	maxLockoutEntries = 10000
	// maxSessions bounds live sessions (relevant in insecure no-password
	// mode, where any request creates one); the least recently seen goes.
	maxSessions = 1024

	globalFailLimit  = 100
	globalFailWindow = time.Minute
	globalLockout    = time.Minute

	janitorEvery   = time.Minute
	maxLoginBody   = 4 << 10
	maxAuditString = 64

	defaultUser = "default"
)

// session is one logged-in browser. id, csrf, user and created are fixed at
// creation; lastSeen is guarded by Console.authMu.
type session struct {
	id, csrf, user string
	created        time.Time
	lastSeen       time.Time

	mu sync.Mutex      // serialises every db.Exec; server.Session is single-goroutine
	db *server.Session // nil when the console has no server (tests)

	history []string // console history, newest first; guarded by mu
	inMulti bool     // an open MULTI on db; guarded by mu
	queued  int      // commands queued in that MULTI; guarded by mu
}

// lockout tracks login failures from one client IP.
type lockout struct {
	failures    int
	inflight    int // attempts reserved but not yet resolved
	windowStart time.Time
	lockedUntil time.Time
	touched     time.Time // last update, for eviction and sweeping
}

type sessionKey struct{}

// sessionOf returns the session attached by the auth middleware, or nil.
func sessionOf(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey{}).(*session)
	return s
}

// randomToken returns 32 bytes of crypto/rand, base64url encoded.
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return base64.RawURLEncoding.EncodeToString(b)
}

// tokenEqual compares secrets in constant time. Hashing first makes the
// comparison independent of the lengths involved.
func tokenEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// parsePrefixes converts validated web-trusted-proxy entries to prefixes.
func parsePrefixes(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// peerAddr returns the TCP peer address of r, or the zero Addr.
func peerAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if a, err := netip.ParseAddr(host); err == nil {
			return a.Unmap()
		}
	}
	return netip.Addr{}
}

// clientIP returns the address used for lockout and audit. X-Forwarded-For
// is honoured only when the peer is a trusted proxy; it is walked from the
// right, skipping further trusted hops, so a client cannot choose its own
// address by prepending entries.
func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	return clientIPLog(r, trusted, nil)
}

// clientIPLog is clientIP with a logger for unparsable hops (nil: silent).
func clientIPLog(r *http.Request, trusted []netip.Prefix, log *slog.Logger) netip.Addr {
	peer := peerAddr(r)
	if !inPrefixes(peer, trusted) {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			if log != nil {
				log.Debug("web unparsable X-Forwarded-For hop", "hop", hops[i])
			}
			return peer // malformed chain: fall back to the proxy itself
		}
		if !inPrefixes(a, trusted) {
			return a
		}
	}
	return peer
}

// parseHop parses one X-Forwarded-For entry: ip, ip:port or [v6]:port.
func parseHop(h string) (netip.Addr, bool) {
	h = strings.TrimSpace(h)
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(h); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// lockoutKey is the lockout bucket for a: the address itself for IPv4, its
// /64 for IPv6 (a single host typically controls a whole /64).
func lockoutKey(a netip.Addr) netip.Prefix {
	if a.Is6() {
		p, _ := a.WithZone("").Prefix(64) // cannot fail for 64 <= 128
		return p
	}
	if !a.IsValid() {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(a, a.BitLen())
}

// isHTTPS reports whether the browser's connection is HTTPS, directly or
// through a trusted proxy.
func (c *Console) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !inPrefixes(peerAddr(r), c.trusted) {
		return false
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// loginRequired reports whether requests need a session obtained by login.
// Read per request so CONFIG SET requirepass takes effect.
func (c *Console) loginRequired() bool {
	return c.cfg.RequirePass() != "" || !c.cfg.WebInsecure
}

// cookieName returns base with the __Host- prefix when the request is HTTPS.
func (c *Console) cookieName(r *http.Request, base string) string {
	if c.isHTTPS(r) {
		return "__Host-" + base
	}
	return base
}

func (c *Console) cookie(r *http.Request, base string) (*http.Cookie, error) {
	return r.Cookie(c.cookieName(r, base))
}

func (c *Console) setCookie(w http.ResponseWriter, r *http.Request, base, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: c.cookieName(r, base), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: c.isHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
}

// newSession creates and stores a session, evicting the least recently seen
// one when the cap is reached.
func (c *Console) newSession(user string) *session {
	now := c.now()
	s := &session{id: randomToken(), csrf: randomToken(), user: user, created: now, lastSeen: now}
	if c.srv != nil {
		s.db = c.srv.NewSession()
		// RESP3 lets the console show maps, doubles and booleans natively. A
		// failure only degrades display to RESP2 shapes, so it is not fatal.
		if _, err := s.db.Exec("HELLO", "3"); err != nil {
			c.log.Warn("web session HELLO 3", "err", err)
		}
	}
	var evicted *session
	c.authMu.Lock()
	if len(c.sessions) >= maxSessions {
		for _, o := range c.sessions {
			if evicted == nil || o.lastSeen.Before(evicted.lastSeen) {
				evicted = o
			}
		}
		delete(c.sessions, evicted.id)
	}
	c.sessions[s.id] = s
	c.authMu.Unlock()
	closeSession(evicted)
	return s
}

// closeSession releases the session's internal client. Close may run while
// another goroutine is inside Exec; server.Session allows that.
func closeSession(s *session) {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

func (c *Console) expired(s *session, now time.Time) bool {
	return now.Sub(s.lastSeen) > c.cfg.WebSessionTimeout || now.Sub(s.created) > absoluteSessionLifetime
}

// sessionByID returns the live session with id, refreshing its idle timer,
// or nil when absent or expired (an expired session is deleted).
func (c *Console) sessionByID(id string) *session {
	now := c.now()
	c.authMu.Lock()
	s := c.sessions[id]
	if s != nil && c.expired(s, now) {
		delete(c.sessions, id)
		c.authMu.Unlock()
		closeSession(s)
		return nil
	}
	if s != nil {
		s.lastSeen = now
	}
	c.authMu.Unlock()
	return s
}

// sessionFrom returns the request's session, or nil when absent or expired.
func (c *Console) sessionFrom(r *http.Request) *session {
	ck, err := c.cookie(r, sessionCookie)
	if err != nil || ck.Value == "" {
		return nil
	}
	return c.sessionByID(ck.Value)
}

func (c *Console) deleteSession(id string) {
	c.authMu.Lock()
	s := c.sessions[id]
	delete(c.sessions, id)
	c.authMu.Unlock()
	closeSession(s)
}

// requireAuth is the session/CSRF middleware. /login and /static/ pass
// through; everything else needs a live session, and every state-changing
// method needs the session's CSRF token.
func (c *Console) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		s := c.sessionFrom(r)
		if s == nil && !c.loginRequired() {
			s = c.newSession(defaultUser)
			c.setCookie(w, r, sessionCookie, s.id, 0)
		}
		if s == nil {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !safeMethod(r.Method) {
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				tok = r.PostFormValue("csrf")
			}
			if tok == "" || !tokenEqual(tok, s.csrf) {
				c.renderError(w, http.StatusForbidden, "Invalid or missing CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)))
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin reports whether an Origin header, if present and not "null",
// matches the scheme and host the browser addressed. Behind a trusted proxy
// those come from X-Forwarded-Proto and X-Forwarded-Host.
func (c *Console) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	scheme, host := "http", r.Host
	if c.isHTTPS(r) {
		scheme = "https"
	}
	if inPrefixes(peerAddr(r), c.trusted) {
		if fh, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); strings.TrimSpace(fh) != "" {
			host = strings.TrimSpace(fh)
		}
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, host)
}

// loginPage renders the login form, issuing a fresh pre-session token.
func (c *Console) loginPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	tok := randomToken()
	c.setCookie(w, r, loginCookie, tok, int(lockoutWindow/time.Second)*2)
	c.render(w, status, "login.html", page{Title: "Sign in", Active: "login", Insecure: c.insecure(), CSRF: tok, Message: msg})
}

func (c *Console) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if s := c.sessionFrom(r); s != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	c.loginPage(w, r, http.StatusOK, "")
}

func (c *Console) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	lc, err := c.cookie(r, loginCookie)
	if !c.sameOrigin(r) || err != nil || lc.Value == "" || !tokenEqual(lc.Value, r.PostFormValue("csrf")) {
		c.renderError(w, http.StatusForbidden, "Login form expired or cross-site request; reload and try again")
		return
	}
	ip := clientIPLog(r, c.trusted, c.log)
	user := r.PostFormValue("username")
	if user == "" {
		user = defaultUser
	}
	auditUser := user
	if len(auditUser) > maxAuditString {
		auditUser = auditUser[:maxAuditString]
	}
	if !c.reserveAttempt(ip) {
		c.log.Info("web login fail", "user", auditUser, "ip", ip.String(), "reason", "locked out")
		c.loginPage(w, r, http.StatusTooManyRequests, "Too many failed attempts; try again later")
		return
	}
	ok := c.checkPass(user, r.PostFormValue("password"))
	c.finishAttempt(ip, ok)
	if !ok {
		c.log.Info("web login fail", "user", auditUser, "ip", ip.String())
		c.loginPage(w, r, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	// Never reuse a pre-existing session ID (fixation).
	if ck, err := c.cookie(r, sessionCookie); err == nil {
		c.deleteSession(ck.Value)
	}
	s := c.newSession(defaultUser)
	c.setCookie(w, r, sessionCookie, s.id, 0)
	c.setCookie(w, r, loginCookie, "", -1)
	c.log.Info("web login ok", "user", auditUser, "ip", ip.String())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// checkPassword validates the default user's password against the current
// requirepass. An empty requirepass never matches: in insecure no-password
// mode login is skipped entirely instead.
func (c *Console) checkPassword(user, pass string) bool {
	want := c.cfg.RequirePass()
	ok := tokenEqual(pass, want)
	return ok && want != "" && user == defaultUser
}

func (c *Console) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s := sessionOf(r); s != nil {
		c.deleteSession(s.id)
	}
	c.setCookie(w, r, sessionCookie, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// reserveAttempt checks the global and per-client locks and, if neither
// applies, reserves an in-flight attempt that counts toward the per-client
// limit until finishAttempt resolves it. Check and reservation share one
// critical section so concurrent requests cannot exceed the limit.
func (c *Console) reserveAttempt(ip netip.Addr) bool {
	now := c.now()
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if now.Before(c.globalLockedUntil) {
		return false
	}
	l := c.lockoutLocked(lockoutKey(ip), now)
	if now.Before(l.lockedUntil) || l.failures+l.inflight >= maxLoginFailures {
		return false
	}
	l.inflight++
	l.touched = now
	return true
}

// finishAttempt resolves a reserved attempt: success clears the client's
// entry, failure records it.
func (c *Console) finishAttempt(ip netip.Addr, ok bool) {
	key := lockoutKey(ip)
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if ok {
		delete(c.lockouts, key)
		return
	}
	if l := c.lockouts[key]; l != nil && l.inflight > 0 {
		l.inflight--
	}
	c.recordFailureLocked(ip)
}

// lockoutLocked returns the entry for key, creating it (and evicting when
// full) as needed and resetting an expired failure window. Caller holds authMu.
func (c *Console) lockoutLocked(key netip.Prefix, now time.Time) *lockout {
	l := c.lockouts[key]
	if l == nil {
		if len(c.lockouts) >= maxLockoutEntries {
			c.evictOldestLockoutLocked()
		}
		l = &lockout{windowStart: now, touched: now}
		c.lockouts[key] = l
	}
	if now.Sub(l.windowStart) > lockoutWindow {
		l.failures, l.windowStart = 0, now
	}
	return l
}

// recordFailureLocked counts a failed login from ip, per client and
// globally. Caller holds authMu.
func (c *Console) recordFailureLocked(ip netip.Addr) {
	now := c.now()
	l := c.lockoutLocked(lockoutKey(ip), now)
	l.failures++
	l.touched = now
	if l.failures >= maxLoginFailures {
		l.lockedUntil = now.Add(lockoutDuration)
		l.failures, l.windowStart = 0, now
	}
	if now.Sub(c.globalWindowStart) > globalFailWindow {
		c.globalFailures, c.globalWindowStart = 0, now
	}
	c.globalFailures++
	if c.globalFailures > globalFailLimit && !now.Before(c.globalLockedUntil) {
		c.globalLockedUntil = now.Add(globalLockout)
		c.globalFailures, c.globalWindowStart = 0, now
		c.log.Warn("web login globally locked: too many failed logins", "failures", globalFailLimit+1, "window", globalFailWindow.String(), "duration", globalLockout.String())
	}
}

// evictOldestLockoutLocked drops the least recently updated entry, preferring
// entries that are not currently locked. Caller holds authMu.
func (c *Console) evictOldestLockoutLocked() {
	now := c.now()
	var victim netip.Prefix
	var vl *lockout
	for ip, l := range c.lockouts {
		locked := now.Before(l.lockedUntil)
		if vl == nil {
			victim, vl = ip, l
			continue
		}
		vlocked := now.Before(vl.lockedUntil)
		if (vlocked && !locked) || (vlocked == locked && l.touched.Before(vl.touched)) {
			victim, vl = ip, l
		}
	}
	delete(c.lockouts, victim)
}

// sweep deletes expired sessions and lockout entries that no longer matter.
func (c *Console) sweep() {
	now := c.now()
	var dead []*session
	c.authMu.Lock()
	for id, s := range c.sessions {
		if c.expired(s, now) {
			delete(c.sessions, id)
			dead = append(dead, s)
		}
	}
	for ip, l := range c.lockouts {
		if l.inflight == 0 && !now.Before(l.lockedUntil) && now.Sub(l.windowStart) > lockoutWindow {
			delete(c.lockouts, ip)
		}
	}
	c.authMu.Unlock()
	for _, s := range dead {
		closeSession(s)
	}
}

// janitor runs sweep every minute until done is closed. Owned by Start/Close.
func (c *Console) janitor(done <-chan struct{}) {
	t := time.NewTicker(janitorEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			c.sweep()
		}
	}
}

// closeAllSessions drops every session; used by Close.
func (c *Console) closeAllSessions() {
	c.authMu.Lock()
	all := c.sessions
	c.sessions = make(map[string]*session)
	c.authMu.Unlock()
	for _, s := range all {
		closeSession(s)
	}
}

// hxHeaders returns the hx-headers JSON that makes htmx send the CSRF token.
func hxHeaders(csrf string) string {
	b, _ := json.Marshal(map[string]string{"X-CSRF-Token": csrf}) // cannot fail for a string map
	return string(b)
}
