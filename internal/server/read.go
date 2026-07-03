package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/techdox/trove/internal/staleness"
)

// ---- response DTOs -------------------------------------------------------

type serviceDTO struct {
	ExternalID       string          `json:"external_id"`
	ParentExternalID string          `json:"parent_external_id,omitempty"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	Image            string          `json:"image"`
	ImageDigest      string          `json:"image_digest,omitempty"`
	State            string          `json:"state"`
	Health           string          `json:"health"`
	Freshness        string          `json:"freshness"` // current | outdated | unknown
	LatestDigest     string          `json:"latest_digest,omitempty"`
	Ports            json.RawMessage `json:"ports"`
	Labels           json.RawMessage `json:"labels"`
	FirstSeenAt      string          `json:"first_seen_at"`
	LastSeenAt       string          `json:"last_seen_at"`
	UpdatedAt        string          `json:"updated_at"`
}

type hostGroupDTO struct {
	Agent       string          `json:"agent"`
	Hostname    string          `json:"hostname"`
	Platform    string          `json:"platform"`
	AgentStatus string          `json:"agent_status"`
	Meta        json.RawMessage `json:"meta"`
	Services    []serviceDTO    `json:"services"`
}

type servicesResponse struct {
	GeneratedAt string         `json:"generated_at"`
	Hosts       []hostGroupDTO `json:"hosts"`
}

type agentDTO struct {
	Name            string  `json:"name"`
	Platform        string  `json:"platform"`
	Version         string  `json:"version"`
	IntervalSeconds int     `json:"interval_seconds"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
	LastSeenAt      *string `json:"last_seen_at"`
}

type agentsResponse struct {
	GeneratedAt string     `json:"generated_at"`
	Agents      []agentDTO `json:"agents"`
}

type eventDTO struct {
	Kind      string `json:"kind"` // state | health | agent
	Service   string `json:"service,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Agent     string `json:"agent,omitempty"`
	FromState string `json:"from_state"`
	ToState   string `json:"to_state"`
	At        string `json:"at"`
}

type eventsResponse struct {
	GeneratedAt string     `json:"generated_at"`
	Events      []eventDTO `json:"events"`
}

// ---- handlers ------------------------------------------------------------

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListServices(r.Context())
	if err != nil {
		s.log.Error("list services", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load services")
		return
	}
	now := time.Now().UTC()

	// Rows are ordered (agent, host, name); group consecutively by host id.
	var hosts []hostGroupDTO
	byHost := map[int64]int{} // host id -> index in hosts
	for _, row := range rows {
		idx, ok := byHost[row.HostID]
		if !ok {
			hosts = append(hosts, hostGroupDTO{
				Agent:       row.AgentName,
				Hostname:    row.Hostname,
				Platform:    row.AgentPlatform,
				AgentStatus: string(agentStatus(row.AgentLastSeenAt, row.AgentIntervalSeconds, now)),
				Meta:        rawJSON(row.HostMetaJSON, "{}"),
				Services:    []serviceDTO{},
			})
			idx = len(hosts) - 1
			byHost[row.HostID] = idx
		}
		latestDigest := ""
		if row.LatestDigest.Valid {
			latestDigest = row.LatestDigest.String
		}
		freshness := row.FreshnessVerdict()

		hosts[idx].Services = append(hosts[idx].Services, serviceDTO{
			ExternalID:       row.ExternalID,
			ParentExternalID: row.ParentExternalID.String,
			Name:             row.Name,
			Kind:             row.Kind,
			Image:            row.Image,
			ImageDigest:      row.ImageDigest,
			State:            row.State,
			Health:           row.Health,
			Freshness:        freshness,
			LatestDigest:     latestDigest,
			Ports:            rawJSON(row.PortsJSON, "[]"),
			Labels:           rawJSON(row.LabelsJSON, "{}"),
			FirstSeenAt:      rfc3339(row.FirstSeenAt),
			LastSeenAt:       rfc3339(row.LastSeenAt),
			UpdatedAt:        rfc3339(row.UpdatedAt),
		})
	}

	writeJSON(w, http.StatusOK, servicesResponse{
		GeneratedAt: now.Format(time.RFC3339),
		Hosts:       hosts,
	})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		s.log.Error("list agents", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load agents")
		return
	}
	now := time.Now().UTC()
	out := make([]agentDTO, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentDTO{
			Name:            a.Name,
			Platform:        a.Platform,
			Version:         a.Version,
			IntervalSeconds: a.IntervalSeconds,
			Status:          string(agentStatus(a.LastSeenAt, a.IntervalSeconds, now)),
			CreatedAt:       rfc3339(a.CreatedAt),
			LastSeenAt:      rfc3339Ptr(a.LastSeenAt),
		})
	}
	writeJSON(w, http.StatusOK, agentsResponse{
		GeneratedAt: now.Format(time.RFC3339),
		Agents:      out,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	events, err := s.store.RecentEvents(r.Context(), limit)
	if err != nil {
		s.log.Error("recent events", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load events")
		return
	}
	out := make([]eventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, eventDTO{
			Kind:      e.Kind,
			Service:   e.Service,
			Hostname:  e.Hostname,
			Agent:     e.Agent,
			FromState: e.FromState,
			ToState:   e.ToState,
			At:        rfc3339(e.At),
		})
	}
	writeJSON(w, http.StatusOK, eventsResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Events:      out,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- helpers -------------------------------------------------------------

// agentStatus computes the heartbeat verdict for the /agents strip and the
// per-host badge. It mirrors what the background ticker uses to flag services,
// but is evaluated fresh on every read so the dashboard is always accurate.
func agentStatus(lastSeen sql.NullInt64, intervalSeconds int, now time.Time) staleness.Status {
	var ls *time.Time
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		ls = &t
	}
	return staleness.Evaluate(ls, intervalSeconds, now)
}

func rfc3339(sec int64) string {
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func rfc3339Ptr(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	s := rfc3339(v.Int64)
	return &s
}

// rawJSON returns stored JSON text as json.RawMessage, falling back to a safe
// default if the column is somehow empty.
func rawJSON(s, fallback string) json.RawMessage {
	if s == "" {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(s)
}
