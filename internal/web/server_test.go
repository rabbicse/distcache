package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/config"
)

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'"

func newTestConsole(t *testing.T) *Console {
	t.Helper()
	c, err := New(Options{Config: config.Default(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServer_SecurityHeaders(t *testing.T) {
	h := newTestConsole(t).Handler()
	for _, tc := range []struct {
		name, path string
		noStore    bool
	}{
		{"static", "/static/app.css", false},
		{"login", "/login", true},
		{"notfound", "/nope", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(t, h, tc.path)
			hd := rec.Header()
			if got := hd.Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("CSP: got %q, want %q", got, wantCSP)
			}
			if got := hd.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options: got %q, want nosniff", got)
			}
			if got := hd.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy: got %q, want no-referrer", got)
			}
			if got := hd.Get("Cache-Control") == "no-store"; got != tc.noStore {
				t.Errorf("no-store: got %v, want %v", got, tc.noStore)
			}
		})
	}
}

func TestServer_StaticAssetsEmbedded(t *testing.T) {
	h := newTestConsole(t).Handler()
	for path, ct := range map[string]string{
		"/static/htmx.min.js":     "text/javascript; charset=utf-8",
		"/static/htmx-ext-sse.js": "text/javascript; charset=utf-8",
		"/static/app.css":         "text/css; charset=utf-8",
		"/static/logo-mark.svg":   "image/svg+xml",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got status %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != ct {
			t.Errorf("%s: got Content-Type %q, want %q", path, got, ct)
		}
	}
}

func TestServer_PagesRenderNav(t *testing.T) {
	h := newTestConsole(t).Handler()
	for _, p := range []string{"/", "/keys", "/console", "/pubsub", "/login"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got status %d, want 200", p, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Dashboard", "Keys", "Console", "Pub/Sub", "Logout", "logo-mark.svg"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body missing %q", p, want)
			}
		}
	}
}

func TestServer_PanicRecovered(t *testing.T) {
	c := newTestConsole(t)
	c.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := c.Handler()
	if rec := get(t, h, "/boom"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic route: got status %d, want 500", rec.Code)
	}
	if rec := get(t, h, "/login"); rec.Code != http.StatusOK {
		t.Fatalf("after panic: got status %d, want 200", rec.Code)
	}
}

func TestServer_UnknownRoute404(t *testing.T) {
	rec := get(t, newTestConsole(t).Handler(), "/does/not/exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rec.Code)
	}
}

func TestServer_StartCloseAddr(t *testing.T) {
	cfg := config.Default()
	cfg.WebBind, cfg.WebPort, cfg.WebTLS = "127.0.0.1", 0, "off"
	c, err := New(Options{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	resp, err := http.Get("http://" + c.Addr() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
}
