package agentkit

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

type stubCollector struct {
	snaps []HostSnapshot
	err   error
}

func (s stubCollector) Collect(context.Context) ([]HostSnapshot, error) {
	return s.snaps, s.err
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// A collector that reaches its platform but finds nothing (e.g. a Proxmox
// token without read permission) must warn loudly rather than silently push
// nothing — otherwise the agent looks healthy while the dashboard stays empty.
func TestRunOnceWarnsWhenNothingCollected(t *testing.T) {
	log, buf := newTestLogger()
	p := &pusher{http: http.DefaultClient, serverURL: "http://127.0.0.1:0", token: "t"}

	runOnce(context.Background(), model.ReportAgent{Name: "x"}, stubCollector{snaps: nil}, p, log)

	out := buf.String()
	if !strings.Contains(out, "collected 0 hosts") {
		t.Fatalf("expected a zero-hosts warning, got:\n%s", out)
	}
	if strings.Contains(out, "report pushed") {
		t.Fatalf("must not log 'report pushed' when nothing was collected:\n%s", out)
	}
}

// When there is at least one host, no warning fires and the report is pushed.
func TestRunOncePushesWhenHostsPresent(t *testing.T) {
	var pushes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&pushes, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	log, buf := newTestLogger()
	p := &pusher{http: srv.Client(), serverURL: srv.URL, token: "t"}
	snaps := []HostSnapshot{{
		Host:     model.ReportHost{Hostname: "h1"},
		Services: []model.ReportService{{ExternalID: "c1", Name: "svc", State: "running"}},
	}}

	runOnce(context.Background(), model.ReportAgent{Name: "x"}, stubCollector{snaps: snaps}, p, log)

	out := buf.String()
	if strings.Contains(out, "collected 0 hosts") {
		t.Fatalf("unexpected zero-hosts warning when a host was present:\n%s", out)
	}
	if !strings.Contains(out, "report pushed") {
		t.Fatalf("expected a 'report pushed' log:\n%s", out)
	}
	if n := atomic.LoadInt32(&pushes); n != 1 {
		t.Fatalf("expected exactly 1 push to the server, got %d", n)
	}
}
