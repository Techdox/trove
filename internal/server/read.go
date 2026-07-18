package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/internal/staleness"
	"github.com/techdox/trove/internal/store"
)

// ---- response DTOs -------------------------------------------------------

type serviceDTO struct {
	ID               int64           `json:"id"`
	ExternalID       string          `json:"external_id"`
	ParentExternalID string          `json:"parent_external_id,omitempty"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	Image            string          `json:"image"`
	ImageDigest      string          `json:"image_digest,omitempty"`
	State            string          `json:"state"`
	Health           string          `json:"health"`
	HealthDetail     string          `json:"health_detail,omitempty"` // why unhealthy, when known
	Freshness        string          `json:"freshness"`               // current | outdated | unknown
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
	Status      string          `json:"status"`
	LastSeenAt  *string         `json:"last_seen_at"`
	AgentStatus string          `json:"agent_status"`
	Condition   string          `json:"condition"`
	Metrics     json.RawMessage `json:"metrics"`
	Meta        json.RawMessage `json:"meta"`
	Services    []serviceDTO    `json:"services"`
}

type paginationDTO struct {
	Limit      int  `json:"limit,omitempty"`
	Offset     int  `json:"offset"`
	Count      int  `json:"count"`
	NextOffset *int `json:"next_offset,omitempty"`
}

type servicesResponse struct {
	GeneratedAt string         `json:"generated_at"`
	Hosts       []hostGroupDTO `json:"hosts"`
	Pagination  *paginationDTO `json:"pagination,omitempty"`
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
	ID        int64  `json:"id"`
	ServiceID *int64 `json:"service_id,omitempty"`
	HostID    *int64 `json:"host_id,omitempty"`
	Kind      string `json:"kind"` // state | health | agent | host
	Service   string `json:"service,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Agent     string `json:"agent,omitempty"`
	FromState string `json:"from_state"`
	ToState   string `json:"to_state"`
	At        string `json:"at"`
}

type eventsResponse struct {
	GeneratedAt string         `json:"generated_at"`
	Events      []eventDTO     `json:"events"`
	Pagination  *paginationDTO `json:"pagination,omitempty"`
}

// ---- handlers ------------------------------------------------------------

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parseLimitOffset(r, 0, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.store.ListServicesPage(r.Context(), store.ServiceListOptions{
		Limit:        limit,
		Offset:       offset,
		UpdatedSince: since,
	})
	if err != nil {
		s.log.Error("list services", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load services")
		return
	}
	now := time.Now().UTC()

	// Rows are ordered (agent, host, name); group consecutively by host id.
	var hosts []hostGroupDTO
	byHost := map[int64]int{} // host id -> index in hosts
	// The unpaginated dashboard view includes hosts with no services. This is
	// important for Proxmox nodes, which are reported even when they have no
	// guests, and lets their heartbeat remain visible independently.
	if limit == 0 && offset == 0 && since == 0 {
		knownHosts, err := s.store.ListHosts(r.Context())
		if err != nil {
			s.log.Error("list hosts", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load hosts")
			return
		}
		for _, host := range knownHosts {
			hosts = append(hosts, hostGroupDTO{
				Agent:       host.AgentName,
				Hostname:    host.Hostname,
				Platform:    host.AgentPlatform,
				Status:      string(heartbeatStatus(host.LastSeenAt, host.AgentIntervalSeconds, now)),
				LastSeenAt:  rfc3339Ptr(host.LastSeenAt),
				AgentStatus: string(heartbeatStatus(host.AgentLastSeenAt, host.AgentIntervalSeconds, now)),
				Condition:   host.Condition,
				Metrics:     rawJSON(host.MetricsJSON, "{}"),
				Meta:        rawJSON(host.MetaJSON, "{}"),
				Services:    []serviceDTO{},
			})
			byHost[host.ID] = len(hosts) - 1
		}
	}
	for _, row := range rows {
		idx, ok := byHost[row.HostID]
		if !ok {
			hosts = append(hosts, hostGroupDTO{
				Agent:       row.AgentName,
				Hostname:    row.Hostname,
				Platform:    row.AgentPlatform,
				Status:      string(heartbeatStatus(row.HostLastSeenAt, row.AgentIntervalSeconds, now)),
				LastSeenAt:  rfc3339Ptr(row.HostLastSeenAt),
				AgentStatus: string(heartbeatStatus(row.AgentLastSeenAt, row.AgentIntervalSeconds, now)),
				Condition:   row.HostCondition,
				Metrics:     rawJSON(row.HostMetricsJSON, "{}"),
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
			ID:               row.ID,
			ExternalID:       row.ExternalID,
			ParentExternalID: row.ParentExternalID.String,
			Name:             row.Name,
			Kind:             row.Kind,
			Image:            row.Image,
			ImageDigest:      row.ImageDigest,
			State:            row.State,
			Health:           row.Health,
			HealthDetail:     s.exposedHealthDetail(row.HealthDetail),
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
		Pagination:  pagination(limit, offset, len(rows), limit > 0 || offset > 0 || since > 0),
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
			Status:          string(heartbeatStatus(a.LastSeenAt, a.IntervalSeconds, now)),
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
	limit, offset, err := parseLimitOffset(r, 100, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	events, err := s.store.ListEvents(r.Context(), store.EventListOptions{
		Limit:  limit,
		Offset: offset,
		Since:  since,
		Kind:   kind,
	})
	if err != nil {
		s.log.Error("recent events", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load events")
		return
	}
	out := make([]eventDTO, 0, len(events))
	for _, e := range events {
		var serviceID *int64
		if e.ServiceID.Valid {
			id := e.ServiceID.Int64
			serviceID = &id
		}
		var hostID *int64
		if e.HostID.Valid {
			id := e.HostID.Int64
			hostID = &id
		}
		out = append(out, eventDTO{
			ID:        e.ID,
			ServiceID: serviceID,
			HostID:    hostID,
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
		Pagination:  pagination(limit, offset, len(events), true),
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB().PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	if s.backgroundHealth != nil {
		if err := s.backgroundHealth(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "background worker unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- helpers -------------------------------------------------------------

// heartbeatStatus computes an agent or host heartbeat verdict. It mirrors what
// the background ticker uses to flag services, but is evaluated fresh on every
// read so the dashboard is always accurate.
func heartbeatStatus(lastSeen sql.NullInt64, intervalSeconds int, now time.Time) staleness.Status {
	var ls *time.Time
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		ls = &t
	}
	return staleness.Evaluate(ls, intervalSeconds, now)
}

func parseLimitOffset(r *http.Request, defaultLimit, maxLimit int) (int, int, error) {
	q := r.URL.Query()
	limit := defaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = n
	}
	if offset > 0 && limit == 0 {
		limit = maxLimit
	}
	return limit, offset, nil
}

func parseSince(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("since must be a unix timestamp or RFC3339 time")
		}
		return n, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, fmt.Errorf("since must be a unix timestamp or RFC3339 time")
	}
	return t.UTC().Unix(), nil
}

func pagination(limit, offset, count int, enabled bool) *paginationDTO {
	if !enabled {
		return nil
	}
	out := &paginationDTO{Limit: limit, Offset: offset, Count: count}
	if limit > 0 && count == limit {
		next := offset + limit
		out.NextOffset = &next
	}
	return out
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
