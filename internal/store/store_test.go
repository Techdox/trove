package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/techdox/trove/pkg/model"
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
		Host:     model.ReportHost{Hostname: "host-a", Meta: map[string]string{"docker.version": "29.0"}},
		Services: services,
	}
}

func svc(id, state string, health model.Health) model.ReportService {
	return model.ReportService{
		ExternalID: id, Name: id, Kind: model.KindContainer,
		Image: "img/" + id + ":1", State: state, Health: health,
	}
}

func TestImagesDueForCheckRequiresRunningDigest(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "mixed")

	withDigest := svc("container", "running", model.HealthHealthy)
	withDigest.Image = "ghcr.io/techdox/app:1"
	withDigest.ImageDigest = "sha256:running"

	proxmoxVM := model.ReportService{
		ExternalID: "qemu/101",
		Name:       "winbox",
		Kind:       model.KindVM,
		Image:      "Windows 11",
		State:      "running",
		Health:     model.HealthUnknown,
	}
	localImage := svc("local", "running", model.HealthHealthy)
	localImage.Image = "local/dev:latest"
	localImage.ImageDigest = ""

	if err := st.ApplyReport(ctx, agent.ID, report(withDigest, proxmoxVM, localImage)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	images, err := st.ImagesDueForCheck(ctx, 10)
	if err != nil {
		t.Fatalf("images due: %v", err)
	}
	if len(images) != 1 || images[0] != "ghcr.io/techdox/app:1" {
		t.Fatalf("images due = %+v, want only digest-backed container image", images)
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

	// State + health change on c1 => one state event and one health event.
	*clock = clock.Add(30 * time.Second)
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "exited", model.HealthUnhealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	)); err != nil {
		t.Fatalf("apply 3: %v", err)
	}
	evs, _ := st.RecentEvents(ctx, 100)
	if len(evs) != 4 {
		t.Fatalf("state+health change should add two events, got %d total", len(evs))
	}
	// Newest first: the health event is inserted after the state event.
	if evs[0].Kind != EventKindHealth || evs[0].FromState != "healthy" || evs[0].ToState != "unhealthy" {
		t.Fatalf("newest event = %s %s->%s, want health healthy->unhealthy", evs[0].Kind, evs[0].FromState, evs[0].ToState)
	}
	if evs[1].Kind != EventKindState || evs[1].FromState != "running" || evs[1].ToState != "exited" {
		t.Fatalf("second event = %s %s->%s, want state running->exited", evs[1].Kind, evs[1].FromState, evs[1].ToState)
	}
	// Denormalized display context survives without joins.
	if evs[0].Service != "c1" || evs[0].Hostname != "host-a" || evs[0].Agent != "docker-a" {
		t.Fatalf("event context = %q@%q by %q, want c1@host-a by docker-a", evs[0].Service, evs[0].Hostname, evs[0].Agent)
	}
}

func TestUpdateAgentStatusEvents(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	// First evaluation seeds silently.
	changed, err := st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "ok")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if changed {
		t.Fatal("first status must seed silently, not emit an event")
	}
	// Same status: no event.
	if changed, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "ok"); changed {
		t.Fatal("unchanged status must not emit an event")
	}
	// Transition: event recorded.
	if changed, _ = st.UpdateAgentStatus(ctx, agent.ID, agent.Name, "stale"); !changed {
		t.Fatal("ok->stale must emit an event")
	}
	evs, _ := st.RecentEvents(ctx, 10)
	if len(evs) != 1 || evs[0].Kind != EventKindAgent ||
		evs[0].FromState != "ok" || evs[0].ToState != "stale" || evs[0].Agent != "docker-a" {
		t.Fatalf("unexpected agent event: %+v", evs)
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

func TestPruneRetentionWindows(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	_ = st.ApplyReport(ctx, agent.ID, report(svc("c1", "running", model.HealthHealthy)))
	*clock = clock.Add(30 * time.Second)
	_ = st.ApplyReport(ctx, agent.ID, report()) // c1 soft-removed

	// Ingest no longer prunes: even far in the future, rows persist until a
	// maintenance pass runs.
	*clock = clock.Add(25 * time.Hour)
	_ = st.ApplyReport(ctx, agent.ID, report())
	if rows, _ := st.ListServices(ctx); len(rows) != 1 {
		t.Fatalf("ingest must not prune; got %d rows", len(rows))
	}

	// Removed-retention (24h) has elapsed but event-retention (30d) has not:
	// the removed service goes, the events stay.
	stats, err := st.Prune(ctx,
		int64((30 * 24 * time.Hour).Seconds()),
		int64((24 * time.Hour).Seconds()),
		int64((365 * 24 * time.Hour).Seconds()))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if stats.RemovedServices != 1 {
		t.Fatalf("want 1 removed service pruned, got %d", stats.RemovedServices)
	}
	if rows, _ := st.ListServices(ctx); len(rows) != 0 {
		t.Fatalf("removed service should be pruned, got %d rows", len(rows))
	}
	if evs, _ := st.RecentEvents(ctx, 100); len(evs) == 0 {
		t.Fatal("events within retention must survive the prune")
	}

	// Now advance past event retention: events prune too.
	*clock = clock.Add(31 * 24 * time.Hour)
	stats, err = st.Prune(ctx,
		int64((30 * 24 * time.Hour).Seconds()),
		int64((24 * time.Hour).Seconds()),
		int64((365 * 24 * time.Hour).Seconds()))
	if err != nil {
		t.Fatalf("prune 2: %v", err)
	}
	if stats.Events == 0 {
		t.Fatal("expected old events to be pruned")
	}
	if evs, _ := st.RecentEvents(ctx, 100); len(evs) != 0 {
		t.Fatalf("events past retention should be gone, got %d", len(evs))
	}
}

func TestMarkServicesStaleForHosts(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")
	_ = st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "running", model.HealthHealthy),
		svc("c2", "exited", model.HealthUnhealthy),
	))

	hosts, err := st.ListHosts(ctx)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts: hosts=%+v err=%v", hosts, err)
	}
	n, err := st.MarkServicesStaleForHosts(ctx, []int64{hosts[0].ID})
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
	if n, _ := st.MarkServicesStaleForHosts(ctx, []int64{hosts[0].ID}); n != 0 {
		t.Fatalf("re-mark should change 0 rows, got %d", n)
	}
}

func TestHostHeartbeatIsIndependentWithinAgent(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "proxmox-a")

	hostA := report(svc("vm-a", "running", model.HealthHealthy))
	hostA.Agent.Platform = model.PlatformProxmox
	hostA.Agent.IntervalSeconds = 30
	hostA.Host.Hostname = "node-a"
	if err := st.ApplyReport(ctx, agent.ID, hostA); err != nil {
		t.Fatalf("apply node-a: %v", err)
	}

	*clock = clock.Add(30 * time.Second)
	hostB := report(svc("vm-b", "running", model.HealthHealthy))
	hostB.Agent.Platform = model.PlatformProxmox
	hostB.Agent.IntervalSeconds = 30
	hostB.Host.Hostname = "node-b"
	if err := st.ApplyReport(ctx, agent.ID, hostB); err != nil {
		t.Fatalf("apply node-b: %v", err)
	}

	hosts, err := st.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts = %+v, want node-a and node-b", hosts)
	}
	byName := map[string]Host{}
	for _, host := range hosts {
		byName[host.Hostname] = host
	}
	if got := byName["node-a"].LastSeenAt.Int64; got != clock.Add(-30*time.Second).Unix() {
		t.Fatalf("node-a last_seen_at = %d, want independent original heartbeat", got)
	}
	if got := byName["node-b"].LastSeenAt.Int64; got != clock.Unix() {
		t.Fatalf("node-b last_seen_at = %d, want latest heartbeat", got)
	}

	// node-a crosses the stale threshold while node-b and the shared agent are
	// still healthy. Only node-a's inventory must become stale.
	*clock = clock.Add(61 * time.Second)
	n, err := st.MarkServicesStaleForHosts(ctx, []int64{byName["node-a"].ID})
	if err != nil {
		t.Fatalf("mark node-a stale: %v", err)
	}
	if n != 1 {
		t.Fatalf("stale rows = %d, want 1", n)
	}
	rows, err := st.ListServices(ctx)
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	healthByHost := map[string]string{}
	for _, row := range rows {
		healthByHost[row.Hostname] = row.Health
	}
	if healthByHost["node-a"] != string(model.HealthStale) {
		t.Fatalf("node-a health = %q, want stale", healthByHost["node-a"])
	}
	if healthByHost["node-b"] != string(model.HealthHealthy) {
		t.Fatalf("node-b health = %q, want healthy", healthByHost["node-b"])
	}
}

func TestPruneLongSilentHosts(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "proxmox-a")
	nodeA := report(svc("vm-a", "running", model.HealthHealthy))
	nodeA.Host.Hostname = "node-a"
	if err := st.ApplyReport(ctx, agent.ID, nodeA); err != nil {
		t.Fatalf("apply node-a: %v", err)
	}

	*clock = clock.Add(31 * 24 * time.Hour)
	nodeB := report(svc("vm-b", "running", model.HealthHealthy))
	nodeB.Host.Hostname = "node-b"
	if err := st.ApplyReport(ctx, agent.ID, nodeB); err != nil {
		t.Fatalf("apply node-b: %v", err)
	}
	stats, err := st.Prune(ctx,
		int64((60 * 24 * time.Hour).Seconds()),
		int64((60 * 24 * time.Hour).Seconds()),
		int64((30 * 24 * time.Hour).Seconds()))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if stats.Hosts != 1 {
		t.Fatalf("pruned hosts = %d, want 1", stats.Hosts)
	}
	rows, err := st.ListServices(ctx)
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	if len(rows) != 1 || rows[0].Hostname != "node-b" {
		t.Fatalf("prune removed the active host or retained the silent host: %+v", rows)
	}
	if events, _ := st.RecentEvents(ctx, 10); len(events) != 2 {
		t.Fatalf("host pruning should preserve event history, got %d events", len(events))
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

func TestApplyReportParentChildClearsStaleParent(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "k8s")

	dep := model.ReportService{ExternalID: "deploy/default/web", Name: "web", Kind: model.KindDeployment, State: "1/1", Health: model.HealthHealthy}
	podWithParent := model.ReportService{ExternalID: "pod/default/web-a", ParentExternalID: "deploy/default/web", Name: "web-a", Kind: model.KindPod, State: "running", Health: model.HealthHealthy}
	if err := st.ApplyReport(ctx, agent.ID, report(dep, podWithParent)); err != nil {
		t.Fatalf("apply with parent: %v", err)
	}

	podStandalone := podWithParent
	podStandalone.ParentExternalID = ""
	if err := st.ApplyReport(ctx, agent.ID, report(dep, podStandalone)); err != nil {
		t.Fatalf("apply standalone: %v", err)
	}

	rows, _ := st.ListServices(ctx)
	byID := map[string]ServiceRow{}
	for _, r := range rows {
		byID[r.ExternalID] = r
	}
	if got := byID["pod/default/web-a"].ParentExternalID; got.Valid {
		t.Fatalf("stale parent link persisted as %q", got.String)
	}
}

func TestApplyReportParentChildClearsMissingParent(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "k8s")

	dep := model.ReportService{ExternalID: "deploy/default/web", Name: "web", Kind: model.KindDeployment, State: "1/1", Health: model.HealthHealthy}
	pod := model.ReportService{ExternalID: "pod/default/web-a", ParentExternalID: dep.ExternalID, Name: "web-a", Kind: model.KindPod, State: "running", Health: model.HealthHealthy}
	if err := st.ApplyReport(ctx, agent.ID, report(dep, pod)); err != nil {
		t.Fatalf("apply with parent: %v", err)
	}

	pod.ParentExternalID = "deploy/default/missing"
	if err := st.ApplyReport(ctx, agent.ID, report(pod)); err != nil {
		t.Fatalf("apply with missing parent: %v", err)
	}

	rows, _ := st.ListServices(ctx)
	for _, row := range rows {
		if row.ExternalID == pod.ExternalID && row.ParentExternalID.Valid {
			t.Fatalf("stale parent link persisted as %q", row.ParentExternalID.String)
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
