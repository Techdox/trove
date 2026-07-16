package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/pkg/model"
)

func TestReadAPIsExposeStableServiceIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, agent, err := st.CreateAgent(ctx, "docker-a")
	if err != nil {
		t.Fatal(err)
	}
	report := func(state string) *model.Report {
		cpuUsage := 0.25
		return &model.Report{
			Agent: model.ReportAgent{Name: "docker-a", Platform: model.PlatformDocker, Version: "test"},
			Host: model.ReportHost{
				Hostname:  "host-a",
				Condition: model.HostConditionNormal,
				Metrics: &model.HostMetrics{
					CPUUsageRatio: &cpuUsage,
					Memory:        &model.HostResourceUsage{UsedBytes: 4, TotalBytes: 8},
				},
			},
			Services: []model.ReportService{{
				ExternalID: "container-1", Name: "web", Kind: model.KindContainer,
				State: state, Health: model.HealthHealthy,
			}},
		}
	}
	if err := st.ApplyReport(ctx, agent.ID, report("running")); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyReport(ctx, agent.ID, report("exited")); err != nil {
		t.Fatal(err)
	}

	srv := New(st, slog.Default())
	servicesReq := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	servicesRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(servicesRec, servicesReq)
	if servicesRec.Code != http.StatusOK {
		t.Fatalf("services status = %d", servicesRec.Code)
	}
	var services struct {
		Hosts []struct {
			Status     string            `json:"status"`
			LastSeenAt *string           `json:"last_seen_at"`
			Condition  string            `json:"condition"`
			Metrics    model.HostMetrics `json:"metrics"`
			Services   []struct {
				ID int64 `json:"id"`
			} `json:"services"`
		} `json:"hosts"`
	}
	if err := json.NewDecoder(servicesRec.Body).Decode(&services); err != nil {
		t.Fatal(err)
	}
	if len(services.Hosts) != 1 || len(services.Hosts[0].Services) != 1 || services.Hosts[0].Services[0].ID == 0 {
		t.Fatalf("services response omitted stable id: %+v", services)
	}
	if services.Hosts[0].Status != "ok" || services.Hosts[0].LastSeenAt == nil {
		t.Fatalf("services response omitted host liveness: %+v", services.Hosts[0])
	}
	if services.Hosts[0].Condition != "normal" || services.Hosts[0].Metrics.CPUUsageRatio == nil ||
		*services.Hosts[0].Metrics.CPUUsageRatio != 0.25 || services.Hosts[0].Metrics.Memory == nil {
		t.Fatalf("services response omitted host condition/metrics: %+v", services.Hosts[0])
	}
	serviceID := services.Hosts[0].Services[0].ID

	eventsReq := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	eventsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(eventsRec, eventsReq)
	if eventsRec.Code != http.StatusOK {
		t.Fatalf("events status = %d", eventsRec.Code)
	}
	var events struct {
		Events []struct {
			ServiceID *int64 `json:"service_id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(eventsRec.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Events {
		if event.ServiceID != nil && *event.ServiceID == serviceID {
			return
		}
	}
	t.Fatalf("events response omitted matching service id: %+v", events)
}

func TestEvaluateStalenessUsesHostHeartbeat(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, agent, err := st.CreateAgent(ctx, "proxmox-a")
	if err != nil {
		t.Fatal(err)
	}
	reportHost := func(hostname, serviceID string) *model.Report {
		return &model.Report{
			Agent: model.ReportAgent{
				Name:            "proxmox-a",
				Platform:        model.PlatformProxmox,
				IntervalSeconds: 30,
			},
			Host: model.ReportHost{Hostname: hostname},
			Services: []model.ReportService{{
				ExternalID: serviceID,
				Name:       serviceID,
				Kind:       model.KindVM,
				State:      "running",
				Health:     model.HealthHealthy,
			}},
		}
	}
	if err := st.ApplyReport(ctx, agent.ID, reportHost("node-a", "vm-a")); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyReport(ctx, agent.ID, reportHost("node-b", "vm-b")); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE hosts SET last_seen_at = ?`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now, agent.ID); err != nil {
		t.Fatal(err)
	}
	srv := New(st, slog.Default())
	srv.evaluateStaleness(ctx) // silently seed agent and host transition state

	if _, err := st.DB().ExecContext(ctx,
		`UPDATE hosts SET last_seen_at = ? WHERE hostname = 'node-a'`, now-4*60); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE hosts SET last_seen_at = ? WHERE hostname = 'node-b'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now, agent.ID); err != nil {
		t.Fatal(err)
	}

	srv.evaluateStaleness(ctx)
	rows, err := st.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	healthByHost := map[string]string{}
	for _, row := range rows {
		healthByHost[row.Hostname] = row.Health
	}
	if healthByHost["node-a"] != string(model.HealthStale) {
		t.Fatalf("node-a service health = %q, want stale", healthByHost["node-a"])
	}
	if healthByHost["node-b"] != string(model.HealthHealthy) {
		t.Fatalf("node-b service health = %q, want healthy", healthByHost["node-b"])
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("services status = %d", rec.Code)
	}
	var response struct {
		Hosts []struct {
			Hostname    string `json:"hostname"`
			Status      string `json:"status"`
			AgentStatus string `json:"agent_status"`
		} `json:"hosts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	statusByHost := map[string]string{}
	for _, host := range response.Hosts {
		if host.AgentStatus != "ok" {
			t.Fatalf("agent status for %s = %q, want ok", host.Hostname, host.AgentStatus)
		}
		statusByHost[host.Hostname] = host.Status
	}
	if statusByHost["node-a"] != "stale" || statusByHost["node-b"] != "ok" {
		t.Fatalf("host statuses = %+v, want node-a stale and node-b ok", statusByHost)
	}
	events, err := st.ListEvents(ctx, store.EventListOptions{Kind: store.EventKindHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Hostname != "node-a" ||
		events[0].FromState != "ok" || events[0].ToState != "stale" {
		t.Fatalf("host events = %+v, want node-a ok->stale", events)
	}
}

func TestEvaluateStalenessSuppressesHostEventsDuringAgentOutage(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, agent, err := st.CreateAgent(ctx, "proxmox-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyReport(ctx, agent.ID, &model.Report{
		Agent: model.ReportAgent{
			Name:            "proxmox-a",
			Platform:        model.PlatformProxmox,
			IntervalSeconds: 30,
		},
		Host: model.ReportHost{Hostname: "node-a"},
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE hosts SET last_seen_at = ? WHERE agent_id = ?`, now, agent.ID); err != nil {
		t.Fatal(err)
	}
	srv := New(st, slog.Default())
	srv.evaluateStaleness(ctx) // silent ok seed

	// The entire source goes stale: emit one agent transition and suppress the
	// equivalent host transition.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now-4*60, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE hosts SET last_seen_at = ? WHERE agent_id = ?`, now-4*60, agent.ID); err != nil {
		t.Fatal(err)
	}
	srv.evaluateStaleness(ctx)
	hostEvents, err := st.ListEvents(ctx, store.EventListOptions{Kind: store.EventKindHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(hostEvents) != 0 {
		t.Fatalf("whole-agent outage emitted duplicate host events: %+v", hostEvents)
	}
	agentEvents, err := st.ListEvents(ctx, store.EventListOptions{Kind: store.EventKindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if len(agentEvents) != 1 || agentEvents[0].ToState != "stale" {
		t.Fatalf("agent events = %+v, want one stale event", agentEvents)
	}

	// The agent comes back but its host does not. The held host transition now
	// becomes independently actionable.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`, now, agent.ID); err != nil {
		t.Fatal(err)
	}
	srv.evaluateStaleness(ctx)
	hostEvents, err = st.ListEvents(ctx, store.EventListOptions{Kind: store.EventKindHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(hostEvents) != 1 || hostEvents[0].FromState != "ok" || hostEvents[0].ToState != "stale" {
		t.Fatalf("post-recovery host events = %+v, want ok->stale", hostEvents)
	}
}

func TestServicesAPIIncludesEmptyHosts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, agent, err := st.CreateAgent(ctx, "proxmox-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyReport(ctx, agent.ID, &model.Report{
		Agent: model.ReportAgent{
			Name:            "proxmox-a",
			Platform:        model.PlatformProxmox,
			IntervalSeconds: 30,
		},
		Host: model.ReportHost{Hostname: "empty-node"},
	}); err != nil {
		t.Fatal(err)
	}

	srv := New(st, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("services status = %d", rec.Code)
	}
	var response struct {
		Hosts []struct {
			Hostname   string `json:"hostname"`
			Status     string `json:"status"`
			LastSeenAt string `json:"last_seen_at"`
			Services   []any  `json:"services"`
		} `json:"hosts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hosts) != 1 || response.Hosts[0].Hostname != "empty-node" ||
		response.Hosts[0].Status != "ok" || response.Hosts[0].LastSeenAt == "" ||
		len(response.Hosts[0].Services) != 0 {
		t.Fatalf("empty host liveness response = %+v", response.Hosts)
	}
}
