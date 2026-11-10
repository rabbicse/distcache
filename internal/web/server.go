// Package web implements the DistCache web console: an HTTP server, separate
// from the RESP listener, that serves an embedded htmx-driven UI.
//
// This file holds the Console lifecycle (construction, listening, shutdown),
// routing and page rendering. Security headers and panic recovery live in
// middleware.go. Authentication (Task 5) and TLS material (Task 4) plug into
// the hook points marked below.
package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Options configures a Console.
type Options struct {
	// Server is the DistCache server the console inspects and drives.
	Server *server.Server
	// Config supplies the web-* settings.
	Config *config.Config
	// Log receives console log output; nil selects slog.Default.
	Log *slog.Logger
}

// Console is the web console HTTP server.
type Console struct {
	srv *server.Server
	cfg *config.Config
	log *slog.Logger

	tmpl *template.Template
	mux  *http.ServeMux // routes; handler wraps it with the middleware chain
	h    http.Handler

	// mu guards ln and hs, which Start and Close set and Addr reads.
	mu sync.Mutex
	ln net.Listener
	hs *http.Server
}

// page is the data passed to layout.html for every page.
type page struct {
	Title    string
	Active   string // nav entry to highlight
	Insecure bool   // show the insecure-banner slot
	Status   int
	Message  string
}

// New builds the console handler and parses templates. It does not listen.
func New(o Options) (*Console, error) {
	if o.Config == nil {
		return nil, errors.New("web: nil config")
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("embed static: %w", err)
	}
	c := &Console{srv: o.Server, cfg: o.Config, log: log, tmpl: tmpl, mux: http.NewServeMux()}

	c.mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	c.mux.HandleFunc("GET /{$}", c.pageHandler("Dashboard", "dashboard"))
	c.mux.HandleFunc("GET /keys", c.pageHandler("Keys", "keys"))
	c.mux.HandleFunc("GET /console", c.pageHandler("Console", "console"))
	c.mux.HandleFunc("GET /pubsub", c.pageHandler("Pub/Sub", "pubsub"))
	c.mux.HandleFunc("GET /login", c.pageHandler("Login", "login"))
	c.mux.HandleFunc("GET /logout", c.pageHandler("Logout", "logout"))
	c.mux.HandleFunc("/", c.notFound)

	// Chain order: recover -> headers -> (session/CSRF, Task 5) -> mux.
	var h http.Handler = c.mux
	// Task 5 wraps h with the session and CSRF middleware here.
	h = securityHeaders(h)
	h = recoverer(log, c.renderError, h)
	c.h = h
	return c, nil
}

// Handler returns the full middleware-wrapped handler, for httptest.
func (c *Console) Handler() http.Handler { return c.h }

// resolveTLS returns the TLS configuration for the listener, or nil for plain
// HTTP. Task 4 implements certificate loading and self-signed generation; until
// then the console always serves plain HTTP.
func (c *Console) resolveTLS() (*tls.Config, error) { return nil, nil }

// Start binds WebBind:WebPort and serves in the background. It returns once
// the socket is bound. Callers skip Start when WebPort is 0 (console disabled).
func (c *Console) Start() error {
	tlsCfg, err := c.resolveTLS()
	if err != nil {
		return fmt.Errorf("web tls: %w", err)
	}
	addr := net.JoinHostPort(c.cfg.WebBind, strconv.Itoa(c.cfg.WebPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web listen %s: %w", addr, err)
	}
	hs := &http.Server{
		Handler:           c.h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(c.log.Handler(), slog.LevelWarn),
	}
	c.mu.Lock()
	c.ln, c.hs = ln, hs
	c.mu.Unlock()

	// The goroutine ends when Close shuts hs down.
	go func() {
		var err error
		if tlsCfg != nil {
			err = hs.ServeTLS(ln, "", "")
		} else {
			err = hs.Serve(ln)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("web console stopped", "err", err)
		}
	}()
	return nil
}

// Addr returns the bound listen address, or "" before Start.
func (c *Console) Addr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ln == nil {
		return ""
	}
	return c.ln.Addr().String()
}

// Close gracefully stops the listener; it is a no-op before Start.
func (c *Console) Close() error {
	c.mu.Lock()
	hs := c.hs
	c.mu.Unlock()
	if hs == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(ctx); err != nil {
		return hs.Close()
	}
	return nil
}

// pageHandler returns a handler that renders a placeholder page in the layout.
func (c *Console) pageHandler(title, active string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.render(w, http.StatusOK, "layout.html", page{Title: title, Active: active, Insecure: c.insecure()})
	}
}

// insecure reports whether the banner slot should show: plain HTTP beyond
// loopback. Task 4 refines this once TLS state is known.
func (c *Console) insecure() bool { return c.cfg.WebInsecure }

func (c *Console) notFound(w http.ResponseWriter, r *http.Request) {
	c.renderError(w, http.StatusNotFound, "Page not found")
}

// renderError writes the error page with the given status.
func (c *Console) renderError(w http.ResponseWriter, status int, msg string) {
	c.render(w, status, "error.html", page{Title: strconv.Itoa(status), Status: status, Message: msg, Insecure: c.insecure()})
}

// render executes a template into a buffer first so a template failure can
// still produce a clean 500 instead of a half-written page.
func (c *Console) render(w http.ResponseWriter, status int, name string, p page) {
	var buf bytes.Buffer
	if err := c.tmpl.ExecuteTemplate(&buf, name, p); err != nil {
		c.log.Error("render template", "name", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // client write errors are not actionable
}

// staticHandler serves embedded assets with explicit Content-Type values so
// the result does not depend on the host's mime database.
func staticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := staticType(path.Ext(r.URL.Path)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}

func staticType(ext string) string {
	switch ext {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	return mime.TypeByExtension(ext)
}
