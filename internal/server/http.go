package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// errorBody is the shape of every error response.
type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// withRecover turns a panic in any handler into a 500 instead of dropping the
// connection, and logs it.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic in handler", "err", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders applies browser defenses to every response and prevents
// authenticated or otherwise dynamic endpoints from being stored by browsers
// or intermediary caches. HSTS is intentionally left to the TLS terminator:
// Trove also supports direct HTTP deployments on trusted networks.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &securityResponseWriter{ResponseWriter: w, noStore: noStorePath(r.URL.Path)}
		sw.apply()
		next.ServeHTTP(sw, r)
	})
}

// securityResponseWriter reapplies the headers immediately before the response
// is committed. net/http's file server deliberately removes Cache-Control on
// some error paths, but dynamic and authentication-related errors must not
// become cacheable either.
type securityResponseWriter struct {
	http.ResponseWriter
	noStore bool
	wrote   bool
}

func (w *securityResponseWriter) apply() {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if w.noStore {
		w.Header().Set("Cache-Control", "no-store")
	}
}

func (w *securityResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.apply()
	w.ResponseWriter.WriteHeader(status)
}

func (w *securityResponseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap allows http.ResponseController to reach optional interfaces provided
// by the original writer.
func (w *securityResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func noStorePath(path string) bool {
	return path == "/" || path == "/index.html" || path == "/metrics" || path == "/healthz" ||
		strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/oauth2/")
}
