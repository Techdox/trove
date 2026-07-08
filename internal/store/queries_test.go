package store

import (
	"context"
	"testing"
	"time"

	"github.com/techdox/trove/pkg/model"
)

func TestListEventsFilteringAndPagination(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "running", model.HealthHealthy),
	)); err != nil {
		t.Fatalf("apply initial: %v", err)
	}
	*clock = clock.Add(time.Hour)
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("c1", "exited", model.HealthUnhealthy),
	)); err != nil {
		t.Fatalf("apply change: %v", err)
	}

	events, err := st.ListEvents(ctx, EventListOptions{Limit: 1, Offset: 0, Since: clock.Unix(), Kind: "state"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].ToState != "exited" {
		t.Fatalf("events = %+v, want newest state change only", events)
	}
}

func TestListServicesPageFiltersByUpdatedAt(t *testing.T) {
	st, clock := newTestStore(t)
	ctx := context.Background()
	_, agent, _ := st.CreateAgent(ctx, "docker-a")

	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("old", "running", model.HealthHealthy),
	)); err != nil {
		t.Fatalf("apply old: %v", err)
	}
	cutoff := clock.Add(time.Hour)
	*clock = cutoff
	if err := st.ApplyReport(ctx, agent.ID, report(
		svc("old", "running", model.HealthHealthy),
		svc("new", "running", model.HealthHealthy),
	)); err != nil {
		t.Fatalf("apply new: %v", err)
	}

	rows, err := st.ListServicesPage(ctx, ServiceListOptions{Limit: 1, UpdatedSince: cutoff.Unix()})
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want limit 1", len(rows))
	}
}
