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
	"strconv"
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

type namedDispatcher struct {
	name string
	Dispatcher
}

func (d namedDispatcher) Name() string { return d.name }

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
		Kinds:      map[string]bool{"agent": true, "host": true, "health": true, "state": true, "freshness": true},
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

func TestDefaultAlertKindsIncludeHostLiveness(t *testing.T) {
	t.Setenv("TROVE_ALERT_EVENTS", "")
	cfg := LoadConfigFromEnv()
	if !cfg.Kinds[store.EventKindHost] {
		t.Fatalf("default alert kinds = %+v, want host liveness enabled", cfg.Kinds)
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

func TestEngineHostLifecycle(t *testing.T) {
	e, st, s, _ := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "proxmox-a")
	rep := testReport()
	rep.Agent.Name = "proxmox-a"
	rep.Agent.Platform = model.PlatformProxmox
	rep.Host.Hostname = "node-a"
	if err := st.ApplyReport(ctx, agent.ID, rep); err != nil {
		t.Fatalf("apply report: %v", err)
	}
	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts: hosts=%+v err=%v", hosts, err)
	}
	host := hosts[0]
	if _, err := st.UpdateHostStatus(ctx, host.ID, agent.ID, host.Hostname, agent.Name, "ok"); err != nil {
		t.Fatalf("seed host status: %v", err)
	}
	e.Sweep(ctx) // seed event cursor

	if _, err := st.UpdateHostStatus(ctx, host.ID, agent.ID, host.Hostname, agent.Name, "stale"); err != nil {
		t.Fatalf("mark host stale: %v", err)
	}
	e.Sweep(ctx)
	if got := s.titles(); len(got) != 1 || got[0] != "warning: host node-a stale" {
		t.Fatalf("stale alert = %v, want host warning", got)
	}

	s.reset()
	if _, err := st.UpdateHostStatus(ctx, host.ID, agent.ID, host.Hostname, agent.Name, "offline"); err != nil {
		t.Fatalf("mark host offline: %v", err)
	}
	e.Sweep(ctx)
	if got := s.titles(); len(got) != 1 || got[0] != "critical: host node-a offline" {
		t.Fatalf("offline alert = %v, want host critical escalation", got)
	}

	s.reset()
	if _, err := st.UpdateHostStatus(ctx, host.ID, agent.ID, host.Hostname, agent.Name, "ok"); err != nil {
		t.Fatalf("recover host: %v", err)
	}
	e.Sweep(ctx)
	if got := s.titles(); len(got) != 1 || got[0] != "resolved: host node-a recovered" {
		t.Fatalf("recovery alert = %v, want host resolution", got)
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

func TestEngineRetriesEventWhenAllDispatchersFail(t *testing.T) {
	e, st, s, _ := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx) // seed cursor

	s.fail = true
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx)
	failedAttempts := s.count()
	if failedAttempts == 0 {
		t.Fatal("want at least one failed delivery attempt")
	}

	rows, err := st.ListServices(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list services: rows=%+v err=%v", rows, err)
	}
	key := "svc:" + strconv.FormatInt(rows[0].ID, 10) + ":health"
	state, seen, err := st.GetAlertState(ctx, key)
	if err != nil {
		t.Fatalf("get alert state: %v", err)
	}
	if seen && state.Notified {
		t.Fatalf("failed delivery was marked notified: %+v", state)
	}

	s.fail = false
	e.Sweep(ctx)
	if s.count() != failedAttempts+1 {
		t.Fatalf("want one successful retry after dispatcher recovers, got titles: %v", s.titles())
	}
	state, seen, err = st.GetAlertState(ctx, key)
	if err != nil || !seen || !state.Notified || state.Value != "unhealthy" {
		t.Fatalf("successful retry did not mark notified: state=%+v seen=%v err=%v", state, seen, err)
	}
}

func TestEngineRetriesOnlyFailedChannelAfterPartialFanout(t *testing.T) {
	e, st, good, _ := newTestEngine(t)
	ctx := context.Background()
	flaky := newSink(t)
	flaky.fail = true
	e.dispatchers = []Dispatcher{
		namedDispatcher{name: "good", Dispatcher: &webhookDispatcher{client: good.srv.Client(), url: good.srv.URL}},
		namedDispatcher{name: "flaky", Dispatcher: &webhookDispatcher{client: flaky.srv.Client(), url: flaky.srv.URL}},
	}
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx) // seed cursor
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if good.count() != 1 || flaky.count() != 3 { // dispatcher retries each failure three times
		t.Fatalf("first fanout good=%d flaky=%d, want 1/3", good.count(), flaky.count())
	}

	flaky.fail = false
	e.Sweep(ctx)
	if good.count() != 1 {
		t.Fatalf("successful channel was duplicated during retry: %d sends", good.count())
	}
	if flaky.count() != 4 {
		t.Fatalf("failed channel was not retried after recovery: %d sends", flaky.count())
	}
}

// TestEngineReconnectAfterMassStaleDoesNotDropNotifiedBit reproduces a
// specific pre-fix bug: when a host goes stale, MarkServicesStaleForHosts
// mass-flips every live service's health to "stale" directly in SQL (no
// event, by design — staleness is surfaced at host level instead of emitting
// an event per service).
// When the host reconnects and reports the SAME still-unhealthy service,
// ApplyReport diffs against that clobbered "stale" baseline and synthesizes a
// fresh stale->unhealthy event, even though nothing actually changed from the
// operator's perspective. Before the fix, deliver() treated that replay as a
// brand-new bad transition and re-stamped the key as "suppressed", erasing
// the fact that the original incident had already been announced — so the
// eventual real recovery went out silently. This test locks in: exactly one
// alert for the original incident, zero for the reconnect replay, and exactly
// one resolved notice for the real recovery.
func TestEngineReconnectAfterMassStaleDoesNotDropNotifiedBit(t *testing.T) {
	e, st, s, clock := newTestEngine(t)
	ctx := context.Background()
	agentToken, agent, _ := st.CreateAgent(ctx, "docker-a")
	_ = agentToken

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx) // seed

	// Service goes unhealthy: one real, notified alert.
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 1 {
		t.Fatalf("want 1 unhealthy alert, got: %v", s.titles())
	}

	// Host goes stale shortly after (well within cooldown): mass-flip, no event.
	*clock = clock.Add(time.Second)
	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts: hosts=%+v err=%v", hosts, err)
	}
	if _, err := st.MarkServicesStaleForHosts(ctx, []int64{hosts[0].ID}); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	s.reset()
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("mass stale flip must not itself alert, got: %v", s.titles())
	}

	// Agent reconnects, reports the service still unhealthy (unchanged from
	// the operator's view, but a diff against the clobbered "stale" baseline).
	*clock = clock.Add(time.Second)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("reconnect replay of an already-announced incident must not re-alert, got: %v", s.titles())
	}

	// It genuinely recovers: must get exactly one resolved notice.
	*clock = clock.Add(time.Second)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)
	if s.count() != 1 || s.titles()[0][:8] != "resolved" {
		t.Fatalf("want exactly 1 resolved notice for the real recovery, got: %v", s.titles())
	}
}

// TestSweepFreshnessSurvivesUnknownBlip reproduces a specific pre-fix bug: a
// transient registry error (rate limiting, network blip) makes
// FreshnessVerdict briefly report "unknown" between two "outdated" sweeps.
// Before the fix, the sweep's default branch wrote the observed value
// directly via SetAlertState, bypassing deliver() and losing the "already
// notified" memory — so when the registry recovered and reported "current",
// the resolved notice for the real "outdated" incident was silently dropped.
func TestSweepFreshnessSurvivesUnknownBlip(t *testing.T) {
	e, st, s, clock := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	img := "gitea/gitea:1.22"
	svc := model.ReportService{
		ExternalID: "c1", Name: "gitea", Kind: model.KindContainer,
		Image: img, ImageDigest: "sha256:running", State: "running", Health: model.HealthHealthy,
	}
	_ = st.ApplyReport(ctx, agent.ID, testReport(svc))
	e.Sweep(ctx) // first sight: verdict is "unknown" (no registry data yet), seeds silently
	if s.count() != 0 {
		t.Fatalf("first-sight seed must be silent, got: %v", s.titles())
	}

	// Registry reports a newer digest: real, notified "outdated" alert.
	if err := st.RecordImageDigest(ctx, img, "sha256:newer", clock.Add(time.Hour).Unix()); err != nil {
		t.Fatalf("record digest: %v", err)
	}
	e.Sweep(ctx)
	if s.count() != 1 {
		t.Fatalf("want 1 outdated alert, got: %v", s.titles())
	}

	// Transient registry error: verdict bounces to "unknown".
	s.reset()
	if err := st.RecordImageError(ctx, img, "rate limited", clock.Add(time.Hour).Unix()); err != nil {
		t.Fatalf("record error: %v", err)
	}
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("a registry blip must not itself alert, got: %v", s.titles())
	}

	// Registry recovers and confirms the image is now current: must resolve.
	if err := st.RecordImageDigest(ctx, img, "sha256:running", clock.Add(time.Hour).Unix()); err != nil {
		t.Fatalf("record digest: %v", err)
	}
	e.Sweep(ctx)
	if s.count() != 1 || s.titles()[0][:8] != "resolved" {
		t.Fatalf("want 1 resolved notice surviving the unknown blip, got: %v", s.titles())
	}
}

// TestEngineFlapWithinCooldownStaysConsistent extends the existing flap
// scenario: after a flap is suppressed (bad value observed but not sent
// because a resolve was just announced within the cooldown window), a
// subsequent GENUINE recovery for that same still-unnotified incident must
// stay silent (nothing was ever announced this round, so nothing should be
// "resolved") rather than misreporting recovery state.
func TestEngineFlapWithinCooldownStaysConsistent(t *testing.T) {
	e, st, s, clock := newTestEngine(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)

	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx) // notified
	if s.count() != 1 {
		t.Fatalf("want 1 alert, got: %v", s.titles())
	}

	s.reset()
	*clock = clock.Add(time.Minute)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx) // resolved
	if s.count() != 1 {
		t.Fatalf("want 1 resolved, got: %v", s.titles())
	}

	// Flap back to bad immediately (within cooldown of the resolve): suppressed.
	s.reset()
	*clock = clock.Add(time.Second)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthUnhealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("flap must be suppressed, got: %v", s.titles())
	}

	// It "recovers" again without ever having been announced this round —
	// must stay silent, not claim a resolution that was never communicated.
	s.reset()
	*clock = clock.Add(time.Second)
	_ = st.ApplyReport(ctx, agent.ID, testReport(testSvc("running", model.HealthHealthy)))
	e.Sweep(ctx)
	if s.count() != 0 {
		t.Fatalf("recovery from an unannounced incident must stay silent, got: %v", s.titles())
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
	hid := sql.NullInt64{Int64: 5, Valid: true}
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
		{"host stale", store.EventRow{Kind: "host", HostID: hid, AgentID: aid, Hostname: "node-a", FromState: "ok", ToState: "stale"}, true, LevelWarning},
		{"host offline", store.EventRow{Kind: "host", HostID: hid, AgentID: aid, Hostname: "node-a", FromState: "stale", ToState: "offline"}, true, LevelCritical},
		{"host recovered", store.EventRow{Kind: "host", HostID: hid, AgentID: aid, Hostname: "node-a", FromState: "offline", ToState: "ok"}, true, LevelResolved},
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

func TestClassifyHostKeyUsesImmutableID(t *testing.T) {
	event := store.EventRow{
		Kind:      store.EventKindHost,
		HostID:    sql.NullInt64{Int64: 5, Valid: true},
		AgentID:   sql.NullInt64{Int64: 3, Valid: true},
		Hostname:  "node-a",
		FromState: "ok",
		ToState:   "stale",
	}
	first, _, _, ok := classify(event)
	if !ok {
		t.Fatal("first host event was not classified")
	}
	event.HostID.Int64 = 6 // same agent/name after retention pruning and rediscovery
	second, _, _, ok := classify(event)
	if !ok {
		t.Fatal("second host event was not classified")
	}
	if first == second {
		t.Fatalf("host alert keys collided across immutable IDs: %q", first)
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
