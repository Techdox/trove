package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
		return &model.Report{
			Agent: model.ReportAgent{Name: "docker-a", Platform: model.PlatformDocker, Version: "test"},
			Host:  model.ReportHost{Hostname: "host-a"},
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
			Services []struct {
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
