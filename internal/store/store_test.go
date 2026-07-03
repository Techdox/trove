package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"trove/pkg/model"
)

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// Deterministic, movable clock.
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return clock }
	return st, &clock
}

func report(services ...model.ReportService) *model.Report {
	return &model.Report{
		Agent:    model.ReportAgent{Name: "docker-a", Platform: "docker", Version: "0.1.0"},
		Host:     model.ReportHost{Hostname: "host-a", Meta: map[string]string{"docker_version": "29.0"}},
		Services: services,
	}
}

func svc(id, state string, health model.Health) model.ReportService {
	return model.ReportService{
		ExternalID: id, Name: id, Kind: model.KindContainer,
		Image: "img/" + id + ":1", State: state, Health: health,
	}
}

func TestAgentCreateAndAuthenticate(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	token, agent, err := st.CreateAgent(ctx, "docker-a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if agent.Name != "docker-a" || token == "" {
		t.Fatalf("unexpected create result: %+v token=%q", agent, token)
	}

	got, err := st.AuthenticateByToken(ctx, token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ID != agent.ID {
		t.Fatalf("auth returned agent id %d, want %d", got.ID, agent.ID)
	}

	if _, err := st.AuthenticateByToken(ctx, "trove_wrong"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("wrong token: got %v, want ErrAgentNotFound", err)
	}
	if _, _, err := st.CreateAgent(ctx, "docker-a"); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("dup name: got %v, want ErrAgentExists", err)
	}
}

func TestApplyReportUpsertAndEvents(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	// First report: two services appear.
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "running", model.HealthHealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	)); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	rows, _ := st.ListServices(ctx)
	if len(rows) != 2 {
		t.Fatalf("want 2 services, got %d", len(rows))
	}
	if evs, _ := st.RecentEvents(ctx, 100); len(evs) != 2 {
		t.Fatalf("want 2 appearance events, got %d", len(evs))
	}

	// Idempotent re-apply: no state change => no new events.
	*clock = clock.Add(30 * time.Second)
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "running", model.HealthHealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	)); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if evs, _ := st.RecentEvents(ctx, 100); len(evs) != 2 {
		t.Fatalf("re-apply should add no events, got %d", len(evs))
	}

	// State change on c1 => one new event.
	*clock = clock.Add(30 * time.Second)
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "exited", model.HealthUnhealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	)); err != nil {
		t.Fatalf("apply 3: %v", err)
	}
	evs, _ := st.RecentEvents(ctx, 100)
	if len(evs) != 3 {
		t.Fatalf("state change should add one event, got %d total", len(evs))
	}
	if evs[0].FromState != "running" || evs[0].ToState != "exited" {
		t.Fatalf("newest event = %s->%s, want running->exited", evs[0].FromState, evs[0].ToState)
	}
}

func TestApplyReportSoftRemove(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, report(svc("c1", "running", model.HealthHealthy)))

	// c1 vanishes from the report => soft-removed with a transition event.
	*clock = clock.Add(30 * time.Second)
	_ = st.ApplyReport(ctx, agent.ID, report())

	rows, _ := st.ListServices(ctx)
	if len(rows) != 1 || rows[0].State != model.StateRemoved {
		t.Fatalf("want 1 removed service, got %+v", rows)
	}
	evsAfterRemove, _ := st.RecentEvents(ctx, 100)

	// Applying the empty report again must NOT re-fire the removal event
	// (otherwise updated_at churns and the row never prunes).
	*clock = clock.Add(30 * time.Second)
	_ = st.ApplyReport(ctx, agent.ID, report())
	evsAgain, _ := st.RecentEvents(ctx, 100)
	if len(evsAgain) != len(evsAfterRemove) {
		t.Fatalf("removal event re-fired: %d -> %d", len(evsAfterRemove), len(evsAgain))
	}
}

func TestApplyReportPrunesOldRemoved(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, report(svc("c1", "running", model.HealthHealthy)))
	*clock = clock.Add(30 * time.Second)
	_ = st.ApplyReport(ctx, agent.ID, report()) // c1 removed

	// Jump past the 24h retention window and push again; the removed row prunes.
	*clock = clock.Add(25 * time.Hour)
	_ = st.ApplyReport(ctx, agent.ID, report())
	rows, _ := st.ListServices(ctx)
	if len(rows) != 0 {
		t.Fatalf("expected removed service to be pruned, got %d rows", len(rows))
	}
}

func TestMarkServicesStale(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")
	_ = st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "running", model.HealthHealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	))

	n, err := st.MarkServicesStaleForAgents(ctx, []int64{agent.ID})
	if err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 services flagged stale, got %d", n)
	}
	rows, _ := st.ListServices(ctx)
	for _, r := range rows {
		if r.Health != string(model.HealthStale) {
			t.Fatalf("service %s health = %q, want stale", r.ExternalID, r.Health)
		}
	}

	// Re-marking is a no-op (already stale) => 0 rows changed.
	if n, _ := st.MarkServicesStaleForAgents(ctx, []int64{agent.ID}); n != 0 {
		t.Fatalf("re-mark should change 0 rows, got %d", n)
	}
}

func TestApplyReportParentChild(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "k8s")

	dep := model.ReportService{ExternalID: "deploy/default/web", Name: "web", Kind: model.KindDeployment, State: "3/3", Health: model.HealthHealthy}
	pod := func(id string) model.ReportService {
		return model.ReportService{ExternalID: id, ParentExternalID: "deploy/default/web", Name: id, Kind: model.KindPod, State: "running", Health: model.HealthHealthy}
	}
	if err := st.ApplyReport(ctx, agent.ID, report(dep, pod("pod/default/web-a"), pod("pod/default/web-b"))); err != nil {
		t.Fatalf("apply: %v", err)
	}

	rows, _ := st.ListServices(ctx)
	byID := map[string]ServiceRow{}
	for _, r := range rows {
		byID[r.ExternalID] = r
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 services, got %d", len(rows))
	}
	// Parent has no parent; children point at the deployment's external_id.
	if byID["deploy/default/web"].ParentExternalID.Valid {
		t.Fatal("deployment should have no parent")
	}
	for _, child := range []string{"pod/default/web-a", "pod/default/web-b"} {
		if got := byID[child].ParentExternalID.String; got != "deploy/default/web" {
			t.Fatalf("%s parent = %q, want deploy/default/web", child, got)
		}
	}
}

func TestFreshnessJoinAndDue(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	// Service running a known digest.
	s := svc("c1", "running", model.HealthHealthy)
	s.Image = "gitea/gitea:1.22"
	s.ImageDigest = "sha256:aaa"
	_ = st.ApplyReport(ctx, agent.ID, report(s))

	// Image is due (never checked).
	due, _ := st.ImagesDueForCheck(ctx, 10)
	if len(due) != 1 || due[0] != "gitea/gitea:1.22" {
		t.Fatalf("images due = %v, want [gitea/gitea:1.22]", due)
	}

	// Record latest digest matching what's running => current.
	next := clock.Add(6 * time.Hour).Unix()
	if err := st.RecordImageDigest(ctx, "gitea/gitea:1.22", "sha256:aaa", next); err != nil {
		t.Fatalf("record digest: %v", err)
	}
	rows, _ := st.ListServices(ctx)
	if !rows[0].LatestDigest.Valid || rows[0].LatestDigest.String != "sha256:aaa" {
		t.Fatalf("latest digest not joined: %+v", rows[0].LatestDigest)
	}
	if rows[0].FreshnessStatus.String != "ok" {
		t.Fatalf("freshness status = %q, want ok", rows[0].FreshnessStatus.String)
	}

	// Now no longer due (next_check_at in the future).
	if due, _ := st.ImagesDueForCheck(ctx, 10); len(due) != 0 {
		t.Fatalf("expected nothing due, got %v", due)
	}

	// An error must not blank the previously-good digest.
	if err := st.RecordImageError(ctx, "gitea/gitea:1.22", "boom", clock.Add(time.Hour).Unix()); err != nil {
		t.Fatalf("record error: %v", err)
	}
	rows, _ = st.ListServices(ctx)
	if rows[0].LatestDigest.String != "sha256:aaa" {
		t.Fatalf("error wiped digest: %q", rows[0].LatestDigest.String)
	}
	if rows[0].FreshnessStatus.String != "error" {
		t.Fatalf("status after error = %q, want error", rows[0].FreshnessStatus.String)
	}
}
