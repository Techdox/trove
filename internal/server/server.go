// Package server wires the HTTP surface for the Trove server: the agent
// ingest endpoint (bearer-authenticated), the read-only dashboard APIs, the
// embedded SPA, and the background staleness ticker. All persistence goes
// through internal/store; this package holds no SQL.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/techdox/trove/internal/registry"
	"github.com/techdox/trove/internal/staleness"
	"github.com/techdox/trove/internal/store"
	"github.com/techdox/trove/web"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	store *store.Store
	log   *slog.Logger
	mux   *http.ServeMux

	// stalenessInterval is how often the background ticker re-evaluates agent
	// and host heartbeats and flags services on stale hosts.
	stalenessInterval time.Duration

	// freshness holds the image-freshness checker config; registry is nil
	// until ConfigureFreshness is called.
	freshness FreshnessConfig
	registry  *registry.Client

	// retention drives the maintenance loop (event/removed-service pruning).
	retention RetentionConfig

	// oidc, if non-nil, gates the dashboard + read APIs behind OIDC
	// authentication. Agent ingest and /healthz are never gated.
	oidc *oidcProvider

	// healthDetailsEnabled is an explicit opt-in for storing and returning
	// platform-provided free-form health messages. The default is false.
	healthDetailsEnabled bool

	startTime       time.Time
	reportsAccepted atomic.Uint64
}

// New constructs a Server and registers its routes.
func New(st *store.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		store:             st,
		log:               log,
		mux:               http.NewServeMux(),
		stalenessInterval: 10 * time.Second,
		startTime:         time.Now().UTC(),
	}
	s.routes()
	return s
}

// ConfigureOIDC enables OIDC authentication on the dashboard and read APIs.
// Must be called before the server starts listening. Returns an error if the
// configuration is partial or OIDC discovery fails (e.g. the issuer is
// unreachable).
func (s *Server) ConfigureOIDC(cfg OIDCConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if !cfg.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), oidcDiscoveryTimeout)
	defer cancel()
	provider, err := newOIDCProvider(ctx, cfg, s.log)
	if err != nil {
		return err
	}
	s.oidc = provider
	// Re-register routes with OIDC middleware now active.
	s.routes()
	return nil
}

// Handler returns the HTTP handler for the whole server.
func (s *Server) Handler() http.Handler {
	return withRecover(s.log, s.mux)
}

func (s *Server) routes() {
	s.mux = http.NewServeMux() // reset so ConfigureOIDC can re-register

	// Agent ingest — always authenticated via bearer token, never gated by OIDC.
	s.mux.HandleFunc("POST /api/v1/report", s.requireAgent(s.handleReport))

	// /healthz — never gated (container healthchecks need unauthenticated access).
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Read-only dashboard APIs + SPA. When OIDC is configured, these are
	// wrapped in the auth middleware; otherwise they're open (Phase 1 behavior).
	readAPIs := http.NewServeMux()
	readAPIs.HandleFunc("GET /api/v1/services", s.handleServices)
	readAPIs.HandleFunc("GET /api/v1/agents", s.handleAgents)
	readAPIs.HandleFunc("GET /api/v1/events", s.handleEvents)
	readAPIs.HandleFunc("GET /api/v1/me", s.handleMe)
	readAPIs.HandleFunc("GET /metrics", s.handleMetrics)
	// Embedded SPA. FileServerFS serves index.html for "/" and 404s cleanly
	// for missing assets.
	readAPIs.Handle("GET /", http.FileServerFS(web.FS()))

	var readHandler http.Handler = readAPIs
	if s.oidc != nil {
		// OIDC auth endpoints (not themselves gated).
		s.mux.HandleFunc("GET /oauth2/login", s.oidc.handleOIDCLogin)
		s.mux.HandleFunc("GET /oauth2/callback", s.oidc.handleOIDCCallback)
		s.mux.HandleFunc("POST /oauth2/logout", s.oidc.handleOIDCLogout)
		readHandler = s.oidc.requireAuth(readHandler)
	}

	// Mount the (possibly wrapped) read APIs + SPA at root.
	s.mux.Handle("/", readHandler)
}

// RunStalenessLoop runs the background ticker until ctx is cancelled. It marks
// services belonging to stale/offline hosts as health="stale". Agent status is
// still evaluated independently for the infrastructure summary and events. It
// evaluates once immediately so a freshly started server converges quickly.
func (s *Server) RunStalenessLoop(ctx context.Context) {
	t := time.NewTicker(s.stalenessInterval)
	defer t.Stop()
	s.evaluateStaleness(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.evaluateStaleness(ctx)
		}
	}
}

func (s *Server) evaluateStaleness(ctx context.Context) {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		s.log.Error("staleness: list agents", "err", err)
		return
	}
	now := time.Now().UTC()
	agentStatusByID := make(map[int64]staleness.Status, len(agents))
	for _, a := range agents {
		var lastSeen *time.Time
		if a.LastSeenAt.Valid {
			t := time.Unix(a.LastSeenAt.Int64, 0).UTC()
			lastSeen = &t
		}
		status := staleness.Evaluate(lastSeen, a.IntervalSeconds, now)
		agentStatusByID[a.ID] = status
		// Record heartbeat transitions (ok<->stale<->offline) as agent events
		// for the feed and the alert engine. Never-seen agents are skipped so
		// a freshly minted token doesn't sit in the feed as "unknown".
		if status != staleness.StatusUnknown {
			if changed, err := s.store.UpdateAgentStatus(ctx, a.ID, a.Name, string(status)); err != nil {
				s.log.Error("staleness: record agent status", "agent", a.Name, "err", err)
			} else if changed {
				s.log.Info("agent status changed", "agent", a.Name, "status", status)
			}
		}
	}

	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		s.log.Error("staleness: list hosts", "err", err)
		return
	}
	var staleHostIDs []int64
	for _, h := range hosts {
		var lastSeen *time.Time
		if h.LastSeenAt.Valid {
			t := time.Unix(h.LastSeenAt.Int64, 0).UTC()
			lastSeen = &t
		}
		status := staleness.Evaluate(lastSeen, h.AgentIntervalSeconds, now)
		if staleness.StaleOrWorse(status) {
			staleHostIDs = append(staleHostIDs, h.ID)
		}
		// Host alerts are useful only when the parent agent is healthy. During a
		// whole-agent outage the agent event is the root-cause notification; not
		// advancing host transition state lets a still-missing host alert as soon
		// as the agent recovers or a sibling keeps it healthy.
		if status != staleness.StatusUnknown && agentStatusByID[h.AgentID] == staleness.StatusOK {
			changed, err := s.store.UpdateHostStatus(
				ctx, h.ID, h.AgentID, h.Hostname, h.AgentName, string(status),
			)
			if err != nil {
				s.log.Error("staleness: record host status", "host", h.Hostname, "agent", h.AgentName, "err", err)
			} else if changed {
				s.log.Info("host status changed", "host", h.Hostname, "agent", h.AgentName, "status", status)
			}
		}
	}
	n, err := s.store.MarkServicesStaleForHosts(ctx, staleHostIDs)
	if err != nil {
		s.log.Error("staleness: mark services", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("staleness: flagged services stale", "count", n, "hosts", len(staleHostIDs))
	}
}
