package web

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
)

// contentSecurityPolicy is the policy from the design spec section 4: no
// inline script or style, same-origin resources only.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'"

// securityHeaders sets the response headers every console response carries.
// Everything except static assets is marked no-store so data pages are never
// cached by browsers or proxies.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into a 500 error page so one bad request
// cannot take the console down. The stack goes to the log, never the client.
func recoverer(log *slog.Logger, fail func(http.ResponseWriter, int, string), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Error("web handler panic", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				fail(w, http.StatusInternalServerError, "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
