package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techdox/trove/internal/store"
)

func TestHandlerAppliesSecurityAndCacheHeaders(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, slog.Default())

	for _, path := range []string{"/", "/healthz", "/api/v1/services", "/metrics", "/oauth2/login"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)

			for header, want := range map[string]string{
				"Content-Security-Policy": "frame-ancestors 'none'",
				"Permissions-Policy":      "camera=()",
				"Referrer-Policy":         "no-referrer",
				"X-Content-Type-Options":  "nosniff",
				"X-Frame-Options":         "DENY",
			} {
				if got := rr.Header().Get(header); !strings.Contains(got, want) {
					t.Errorf("%s = %q, want it to contain %q", header, got, want)
				}
			}
			if got := rr.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/styles.css", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if got := rr.Header().Get("Cache-Control"); got == "no-store" {
		t.Fatalf("static asset Cache-Control = %q, should remain cacheable", got)
	}
}

func TestHealthzReportsBackgroundWorkerFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, slog.Default())
	srv.ConfigureBackgroundHealth(func() error { return errors.New("freshness failed") })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rr.Body.String(), "freshness") {
		t.Fatalf("health response exposed internal worker detail: %s", rr.Body.String())
	}
}
