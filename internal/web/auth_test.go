package web

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
)

const testPass = "s3cret-pass"

// fakeClock is a settable clock for Console.now; tests drive it from one
// goroutine.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// newAuthConsole builds a console with requirepass set, a fake clock and a
// captured log. mutate may adjust the config before New.
func newAuthConsole(t *testing.T, mutate func(*config.Config)) (*Console, *fakeClock, *logBuf) {
	t.Helper()
	cfg := config.Default()
	if err := cfg.Set("requirepass", testPass, true); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	log, lb := newBufLogger()
	c, err := New(Options{Config: cfg, Log: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	c.now = clk.now
	t.Cleanup(func() { _ = c.Close() })
	return c, clk, lb
}

// postLogin fetches the login page for its pre-session token, then posts the
// password. mod may adjust the POST request (remote address, headers).
func postLogin(t *testing.T, c *Console, pass string, mod func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	g := httptest.NewRecorder()
	c.Handler().ServeHTTP(g, httptest.NewRequest(http.MethodGet, "/login", nil))
	lc := findCookie(g.Result().Cookies(), loginCookie)
	if lc == nil {
		t.Fatal("GET /login set no login cookie")
	}
	form := url.Values{"username": {"default"}, "password": {pass}, "csrf": {lc.Value}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(lc)
	if mod != nil {
		mod(r)
	}
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, r)
	return rec
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, ck := range cs {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// loginCookieOK logs in and returns the session cookie.
func loginCookieOK(t *testing.T, c *Console) *http.Cookie {
	t.Helper()
	rec := postLogin(t, c, testPass, nil)
	ck := findCookie(rec.Result().Cookies(), sessionCookie)
	if rec.Code != http.StatusSeeOther || ck == nil {
		t.Fatalf("login: got status %d cookie %v, want 303 with %s", rec.Code, ck, sessionCookie)
	}
	return ck
}

func authedGet(c *Console, path string, ck *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if ck != nil {
		r.AddCookie(ck)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, r)
	return rec
}

func TestAuth_LoginSuccessSetsCookie(t *testing.T) {
	tests := []struct {
		name       string
		proxy      []string
		mod        func(*http.Request)
		wantSecure bool
	}{
		{"plain http", nil, nil, false},
		{"direct tls", nil, func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, true},
		{"trusted proxy https", []string{"10.0.0.0/8"}, func(r *http.Request) {
			r.RemoteAddr = "10.1.2.3:4000"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, true},
		{"untrusted proxy https", []string{"10.0.0.0/8"}, func(r *http.Request) {
			r.RemoteAddr = "192.0.2.9:4000"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newAuthConsole(t, func(cfg *config.Config) { cfg.WebTrustedProxy = tc.proxy })
			rec := postLogin(t, c, testPass, tc.mod)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
				t.Fatalf("got %d Location %q, want 303 /", rec.Code, rec.Header().Get("Location"))
			}
			ck := findCookie(rec.Result().Cookies(), sessionCookie)
			if ck == nil {
				t.Fatal("no dc_session cookie")
			}
			if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" {
				t.Errorf("cookie attrs: HttpOnly=%v SameSite=%v Path=%q", ck.HttpOnly, ck.SameSite, ck.Path)
			}
			if ck.Secure != tc.wantSecure {
				t.Errorf("Secure: got %v, want %v", ck.Secure, tc.wantSecure)
			}
		})
	}
}

func TestAuth_WrongPasswordRejected(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	rec := postLogin(t, c, "wrong", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "invalid credentials") {
		t.Error("body lacks generic invalid credentials message")
	}
	if findCookie(rec.Result().Cookies(), sessionCookie) != nil {
		t.Error("session cookie set on failed login")
	}
}

func TestAuth_LoginRejectsCrossOriginAndMissingToken(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	rec := postLogin(t, c, testPass, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin: got %d, want 403", rec.Code)
	}
	form := url.Values{"username": {"default"}, "password": {testPass}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no login token: got %d, want 403", rec.Code)
	}
}

func TestAuth_LockoutAfterFiveFailures(t *testing.T) {
	c, clk, _ := newAuthConsole(t, nil)
	for i := range 5 {
		if rec := postLogin(t, c, "bad", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, rec.Code)
		}
		clk.advance(10 * time.Second)
	}
	if rec := postLogin(t, c, testPass, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: got %d, want 429", rec.Code)
	}
	other := postLogin(t, c, testPass, func(r *http.Request) { r.RemoteAddr = "192.0.2.50:1234" })
	if other.Code != http.StatusSeeOther {
		t.Errorf("other IP: got %d, want 303", other.Code)
	}
	clk.advance(5 * time.Minute)
	if rec := postLogin(t, c, testPass, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("after lockout: got %d, want 303", rec.Code)
	}
}

func TestAuth_FailuresOutsideWindowDoNotLock(t *testing.T) {
	c, clk, _ := newAuthConsole(t, nil)
	for range 4 {
		postLogin(t, c, "bad", nil)
	}
	clk.advance(6 * time.Minute)
	postLogin(t, c, "bad", nil)
	if rec := postLogin(t, c, testPass, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", rec.Code)
	}
}

func TestAuth_LockoutUsesForwardedForOnlyFromTrustedProxy(t *testing.T) {
	c, _, _ := newAuthConsole(t, func(cfg *config.Config) { cfg.WebTrustedProxy = []string{"10.0.0.1"} })
	viaProxy := func(xff string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "10.0.0.1:5555"
			r.Header.Set("X-Forwarded-For", xff)
		}
	}
	for range 5 {
		postLogin(t, c, "bad", viaProxy("198.51.100.7"))
	}
	if rec := postLogin(t, c, testPass, viaProxy("198.51.100.7")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked client via proxy: got %d, want 429", rec.Code)
	}
	if rec := postLogin(t, c, testPass, viaProxy("198.51.100.8")); rec.Code != http.StatusSeeOther {
		t.Fatalf("other client via proxy: got %d, want 303", rec.Code)
	}
	// An untrusted peer cannot dodge the lockout by forging X-Forwarded-For.
	direct := func(xff string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "203.0.113.5:5555"
			r.Header.Set("X-Forwarded-For", xff)
		}
	}
	for i := range 5 {
		postLogin(t, c, "bad", direct("198.51.100."+string(rune('1'+i))))
	}
	if rec := postLogin(t, c, testPass, direct("198.51.100.99")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forged XFF from untrusted peer: got %d, want 429", rec.Code)
	}
}

func TestAuth_ClientIP(t *testing.T) {
	trusted := mustPrefixes(t, []string{"10.0.0.0/8"})
	tests := []struct{ name, remote, xff, want string }{
		{"direct", "192.0.2.1:1", "", "192.0.2.1"},
		{"untrusted ignores xff", "192.0.2.1:1", "1.2.3.4", "192.0.2.1"},
		{"trusted uses xff", "10.0.0.5:1", "1.2.3.4", "1.2.3.4"},
		{"trusted skips trusted hops", "10.0.0.5:1", "1.2.3.4, 5.6.7.8, 10.9.9.9", "5.6.7.8"},
		{"trusted bad xff", "10.0.0.5:1", "garbage", "10.0.0.5"},
		{"mapped v4", "[::ffff:192.0.2.1]:1", "", "192.0.2.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r, trusted).String(); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAuth_CSRFRequiredOnPost(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	c.mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	ck := loginCookieOK(t, c)
	page := authedGet(c, "/", ck, nil)
	s := c.sessionByID(ck.Value)
	if s == nil {
		t.Fatal("session missing")
	}
	if !strings.Contains(page.Body.String(), `<meta name="csrf" content="`+s.csrf+`">`) {
		t.Error("page lacks csrf meta tag")
	}
	if !strings.Contains(page.Body.String(), `hx-headers=`) {
		t.Error("body lacks hx-headers")
	}
	post := func(token, field string) int {
		var body io.Reader
		if field != "" {
			body = strings.NewReader(url.Values{"csrf": {field}}.Encode())
		}
		r := httptest.NewRequest(http.MethodPost, "/echo", body)
		if field != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		r.AddCookie(ck)
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		rec := httptest.NewRecorder()
		c.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	if got := post("", ""); got != http.StatusForbidden {
		t.Errorf("no token: got %d, want 403", got)
	}
	if got := post("wrong", ""); got != http.StatusForbidden {
		t.Errorf("wrong token: got %d, want 403", got)
	}
	if got := post(s.csrf, ""); got != http.StatusOK {
		t.Errorf("header token: got %d, want 200", got)
	}
	if got := post("", s.csrf); got != http.StatusOK {
		t.Errorf("form token: got %d, want 200", got)
	}
}

func TestAuth_UnauthenticatedRedirects(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	rec := authedGet(c, "/keys", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("got %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/login", "/static/app.css"} {
		if rec := authedGet(c, p, nil, nil); rec.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", p, rec.Code)
		}
	}
}

func TestAuth_IdleAndAbsoluteExpiry(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		c, clk, _ := newAuthConsole(t, nil)
		ck := loginCookieOK(t, c)
		clk.advance(c.cfg.WebSessionTimeout - time.Second)
		if rec := authedGet(c, "/", ck, nil); rec.Code != http.StatusOK {
			t.Fatalf("within timeout: got %d, want 200", rec.Code)
		}
		clk.advance(c.cfg.WebSessionTimeout + time.Second)
		if rec := authedGet(c, "/", ck, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Fatalf("idle: got %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("absolute", func(t *testing.T) {
		c, clk, _ := newAuthConsole(t, nil)
		ck := loginCookieOK(t, c)
		for elapsed := time.Duration(0); elapsed < 12*time.Hour; elapsed += 10 * time.Minute {
			clk.advance(10 * time.Minute)
			rec := authedGet(c, "/", ck, nil)
			if elapsed+10*time.Minute <= 12*time.Hour && rec.Code != http.StatusOK {
				t.Fatalf("at %v: got %d, want 200", elapsed+10*time.Minute, rec.Code)
			}
		}
		clk.advance(time.Minute)
		if rec := authedGet(c, "/", ck, nil); rec.Code != http.StatusSeeOther {
			t.Fatalf("past 12h: got %d, want 303", rec.Code)
		}
	})
}

func TestAuth_ExpiredSessionHTMXRedirect(t *testing.T) {
	c, clk, _ := newAuthConsole(t, nil)
	ck := loginCookieOK(t, c)
	clk.advance(c.cfg.WebSessionTimeout + time.Minute)
	rec := authedGet(c, "/keys", ck, map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/login" {
		t.Errorf("HX-Redirect: got %q, want /login", got)
	}
}

func TestAuth_PasswordChangeAppliesToNewLogins(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	old := loginCookieOK(t, c)
	if err := c.cfg.Set("requirepass", "new-pass", false); err != nil {
		t.Fatal(err)
	}
	if rec := postLogin(t, c, testPass, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password: got %d, want 401", rec.Code)
	}
	if rec := postLogin(t, c, "new-pass", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("new password: got %d, want 303", rec.Code)
	}
	if rec := authedGet(c, "/", old, nil); rec.Code != http.StatusOK {
		t.Errorf("existing session: got %d, want 200", rec.Code)
	}
}

func TestAuth_NoPasswordRefusesStart(t *testing.T) {
	cfg := config.Default()
	_, err := New(Options{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if !errors.Is(err, ErrNoPassword) {
		t.Fatalf("got %v, want ErrNoPassword", err)
	}
	if ErrNoPassword.Error() != "web console requires requirepass (or web-insecure yes)" {
		t.Errorf("message: got %q", ErrNoPassword.Error())
	}
	cfg.WebInsecure = true
	c, err := New(Options{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("insecure New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rec := authedGet(c, "/keys", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("insecure no-password page: got %d, want 200", rec.Code)
	}
	if findCookie(rec.Result().Cookies(), sessionCookie) == nil {
		t.Error("insecure mode issued no session cookie")
	}
}

func TestAuth_LogoutDeletesSession(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	ck := loginCookieOK(t, c)
	s := c.sessionByID(ck.Value)
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(ck)
	r.Header.Set("X-CSRF-Token", s.csrf)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("logout: got %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	if cl := findCookie(rec.Result().Cookies(), sessionCookie); cl == nil || cl.MaxAge >= 0 {
		t.Error("logout did not clear the cookie")
	}
	if c.sessionByID(ck.Value) != nil {
		t.Error("session still present after logout")
	}
	if rec := authedGet(c, "/", ck, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("after logout: got %d, want 303", rec.Code)
	}
	if rec := authedGet(c, "/logout", ck, nil); rec.Code == http.StatusSeeOther && rec.Header().Get("Location") == "/" {
		t.Error("GET /logout must not act")
	}
}

func TestAuth_JanitorSweeps(t *testing.T) {
	c, clk, _ := newAuthConsole(t, nil)
	ck := loginCookieOK(t, c)
	postLogin(t, c, "bad", nil)
	clk.advance(13 * time.Hour)
	c.sweep()
	if c.sessionByID(ck.Value) != nil {
		t.Error("expired session survived sweep")
	}
	c.authMu.Lock()
	n := len(c.lockouts)
	c.authMu.Unlock()
	if n != 0 {
		t.Errorf("got %d lockout entries, want 0", n)
	}
}

func TestAuth_LockoutMapBounded(t *testing.T) {
	c, _, _ := newAuthConsole(t, nil)
	c.authMu.Lock()
	for i := range maxLockoutEntries + 50 {
		ip := netipFromInt(i)
		c.recordFailureLocked(ip)
	}
	n := len(c.lockouts)
	c.authMu.Unlock()
	if n > maxLockoutEntries {
		t.Fatalf("got %d entries, want <= %d", n, maxLockoutEntries)
	}
}

func TestAuth_AuditLog(t *testing.T) {
	c, _, lb := newAuthConsole(t, nil)
	postLogin(t, c, "wrong-guess", nil)
	loginCookieOK(t, c)
	out := lb.String()
	if strings.Count(out, "web login fail") != 1 || strings.Count(out, "web login ok") != 1 {
		t.Fatalf("audit lines: %q", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "user=") || !strings.Contains(line, "ip=") {
			t.Errorf("line lacks user/ip: %q", line)
		}
	}
	if strings.Contains(out, testPass) || strings.Contains(out, "wrong-guess") {
		t.Error("password leaked into log")
	}
}

func mustPrefixes(t *testing.T, list []string) []netip.Prefix {
	t.Helper()
	ps, err := parsePrefixes(list)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// netipFromInt returns a distinct IPv4 address for i.
func netipFromInt(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
}
