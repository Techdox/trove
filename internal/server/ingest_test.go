package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/techdox/trove/internal/store"
)

const validReportJSON = `{"agent":{"name":"test-agent","platform":"docker","version":"test"},"host":{"hostname":"test-host"},"services":[]}`

func TestHandleReportRejectsTrailingJSON(t *testing.T) {
	srv, agent := newIngestTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader(validReportJSON+"\n{}"))
	rr := httptest.NewRecorder()

	srv.handleReport(rr, req, agent)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "trailing data") {
		t.Fatalf("body = %s, want trailing-data error", rr.Body.String())
	}
}

func TestHandleReportAcceptsTrailingWhitespace(t *testing.T) {
	srv, agent := newIngestTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader(validReportJSON+"\n\t "))
	rr := httptest.NewRecorder()

	srv.handleReport(rr, req, agent)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHandleReportRejectsOversizedBody(t *testing.T) {
	srv, agent := newIngestTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/report", strings.NewReader(strings.Repeat(" ", maxReportBytes+1)))
	rr := httptest.NewRecorder()

	srv.handleReport(rr, req, agent)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
}

func TestPreviousReleaseReportFixturesRemainCompatible(t *testing.T) {
	files, err := filepath.Glob("testdata/report-v0.15.1-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("previous-release fixtures = %d, want 3", len(files))
	}

	for _, file := range files {
		file := file
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			srv, agent := newIngestTestServer(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(raw))
			rr := httptest.NewRecorder()

			srv.handleReport(rr, req, agent)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
			}
			services, err := srv.store.ListServices(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(services) == 0 {
				t.Fatal("fixture was accepted but stored no services")
			}
		})
	}
}

func newIngestTestServer(t *testing.T) (*Server, store.Agent) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, agent, err := st.CreateAgent(t.Context(), "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	return New(st, slog.Default()), agent
}
