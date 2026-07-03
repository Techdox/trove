package alert

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

// sink collects webhook notifications.
type sink struct {
	mu   sync.Mutex
	got  []Notification
	srv  *httptest.Server
	fail bool
}

func newSink(t *testing.T) *sink {
	t.Helper()
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var n Notification
		_ = json.Unmarshal(body, &n)
		s.mu.Lock()
		s.got = append(s.got, n)
		fail := s.fail
		s.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.got))
	for i, n := range s.got {
		out[i] = n.Level + ": " + n.Title
	}
	return out
}

func (s *sink) reset() { s.mu.Lock(); s.got = nil; s.mu.Unlock() }

func (s *sink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.got) }

func newTestEngine(t *testing.T) (*Engine, *store.Store, *sink, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	s := newSink(t)
	cfg := Config{
		WebhookURL: s.srv.URL,
		Kinds:      map[string]bool{"agent": true, "health": true, "state": true, "freshness": true},
		Cooldown:   5 * time.Minute,
		Interval:   time.Hour, // sweeps are driven manually in tests
	}
	e := NewEngine(st, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return clock }
	return e, st, s, &clock
}

func testReport(services ...model.ReportService) *model.Report {
	return &model.Report{
		Agent:    model.ReportAgent{Name: "docker-a", Platform: "docker", Version: "test"},
		Host:     model.ReportHost{Hostname: "host-a"},
		Services: services,
	}
}

func testSvc(state string, health model.Health) model.ReportService {
	return model.ReportService{
		ExternalID: "c1", Name: "web", Kind: model.KindContainer,
		Image: "nginx:alpine", State: state, Health: health,
	}
}

func TestEngineLifecycle(t *testing.T) {
	e, st, s, clock := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	// Initial report creates appearance events; first sweep seeds the cursor
	// and must send nothing.
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("seed sweep must be silent, sent: %v", s.titles())
	}

	// Service goes down: state + health alerts.
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("exited", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 2 {
		t.Fatalf("want 2 alerts (state warning + health critical), got: %v", s.titles())
	}

	// Nothing new: sweep is idempotent.
	e.Sweep(ctx)
	if s.count() != 2 {
		t.Fatalf("no-op sweep must not resend, got: %v", s.titles())
	}

	// Recovery: two resolved notices.
	s.reset()
	*clock = clock.Add(time.Minute)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)
	titles := s.titles()
	if s.count() != 2 || titles[0][:8] != "resolved" {
		t.Fatalf("want 2 resolved notices, got: %v", titles)
	}

	// Flap within cooldown: suppressed both ways — no storm.
	s.reset()
	*clock = clock.Add(time.Minute)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("exited", model.HealthUnhealthy)))
	e.Sweep(ctx)
	*clock = clock.Add(time.Minute)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("flap within cooldown must be fully suppressed, got: %v", s.titles())
	}

	// After the cooldown expires, a new incident alerts again.
	s.reset()
	*clock = clock.Add(10 * time.Minute)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("exited", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 2 {
		t.Fatalf("post-cooldown incident must alert, got: %v", s.titles())
	}
}

func TestEngineAgentTransitions(t *testing.T) {
	e, st, s, clock := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	e.Sweep(ctx) // seed cursor

	// Seed status silently, then go stale -> warning.
	_, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "ok")
	_, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "stale")
	e.Sweep(ctx)
	if s.count() != 1 {
		t.Fatalf("want 1 stale warning, got: %v", s.titles())
	}

	// Escalation stale -> offline bypasses cooldown (critical).
	_, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "offline")
	e.Sweep(ctx)
	if s.count() != 2 {
		t.Fatalf("offline escalation must bypass cooldown, got: %v", s.titles())
	}

	// Recovery.
	s.reset()
	*clock = clock.Add(10 * time.Minute)
	_, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "ok")
	e.Sweep(ctx)
	if s.count() != 1 || s.titles()[0] != "resolved: agent docker-a recovered" {
		t.Fatalf("want agent recovery notice, got: %v", s.titles())
	}
}

func TestEngineKindFiltering(t *testing.T) {
	e, st, s, _ := newTestEngine(t)
	e.cfg.Kinds = map[string]bool{"agent": true} // only agent alerts
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx) // seed
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("exited", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("state/health disabled; want silence, got: %v", s.titles())
	}
}

func TestClassifyTable(t *testing.T) {
	sid := sql.NullInt64{Int64: 7, Valid: true}
	aid := sql.NullInt64{Int64: 3, Valid: true}
	cases := []struct {
		name      string
		ev        store.EventRow
		wantOK    bool
		wantLevel string
	}{
		{"appearance is feed-only", store.EventRow{Kind: "state", ServiceID: sid, FromState: "", ToState: "running"}, false, ""},
		{"container stop", store.EventRow{Kind: "state", ServiceID: sid, FromState: "running", ToState: "exited"}, true, LevelWarning},
		{"container recover", store.EventRow{Kind: "state", ServiceID: sid, FromState: "exited", ToState: "running"}, true, LevelResolved},
		{"pause is neutral", store.EventRow{Kind: "state", ServiceID: sid, FromState: "running", ToState: "paused"}, false, ""},
		{"k8s degraded", store.EventRow{Kind: "state", ServiceID: sid, FromState: "2/2", ToState: "1/2"}, true, LevelWarning},
		{"k8s fully ready", store.EventRow{Kind: "state", ServiceID: sid, FromState: "1/2", ToState: "2/2"}, true, LevelResolved},
		{"unhealthy", store.EventRow{Kind: "health", ServiceID: sid, FromState: "healthy", ToState: "unhealthy"}, true, LevelCritical},
		{"healthy again", store.EventRow{Kind: "health", ServiceID: sid, FromState: "unhealthy", ToState: "healthy"}, true, LevelResolved},
		{"mass stale flip is feed-only", store.EventRow{Kind: "health", ServiceID: sid, FromState: "healthy", ToState: "stale"}, false, ""},
		{"agent stale", store.EventRow{Kind: "agent", AgentID: aid, FromState: "ok", ToState: "stale"}, true, LevelWarning},
		{"agent offline", store.EventRow{Kind: "agent", AgentID: aid, FromState: "stale", ToState: "offline"}, true, LevelCritical},
		{"agent recovered", store.EventRow{Kind: "agent", AgentID: aid, FromState: "offline", ToState: "ok"}, true, LevelResolved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, n, _, ok := classify(tc.ev)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && n.Level != tc.wantLevel {
				t.Fatalf("level = %q, want %q", n.Level, tc.wantLevel)
			}
		})
	}
}

func TestScheduleNextAfter(t *testing.T) {
	daily, err := ParseSchedule("daily@08:00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 07:00 -> today 08:00; 09:00 -> tomorrow 08:00.
	base := time.Date(2026, 1, 5, 7, 0, 0, 0, time.Local) // a Monday
	if got := daily.NextAfter(base); got.Day() != 5 || got.Hour() != 8 {
		t.Fatalf("daily next from 07:00 = %v", got)
	}
	if got := daily.NextAfter(base.Add(2 * time.Hour)); got.Day() != 6 || got.Hour() != 8 {
		t.Fatalf("daily next from 09:00 = %v", got)
	}

	weekly, err := ParseSchedule("weekly@mon:08:00")
	if err != nil {
		t.Fatalf("parse weekly: %v", err)
	}
	if got := weekly.NextAfter(base); got.Weekday() != time.Monday || got.Day() != 5 {
		t.Fatalf("weekly next from Mon 07:00 = %v", got)
	}
	if got := weekly.NextAfter(base.Add(2 * time.Hour)); got.Weekday() != time.Monday || got.Day() != 12 {
		t.Fatalf("weekly next from Mon 09:00 = %v", got)
	}

	if off, _ := ParseSchedule("off"); !off.Off {
		t.Fatal("'off' must disable the schedule")
	}
	if _, err := ParseSchedule("sometimes@maybe"); err == nil {
		t.Fatal("invalid schedule must error")
	}
}
