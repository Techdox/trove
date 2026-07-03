// Package server wires the HTTP surface for the Trove server: the agent
// ingest endpoint (bearer-authenticated), the read-only dashboard APIs, the
// embedded SPA, and the background staleness ticker. All persistence goes
// through internal/store; this package holds no SQL.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"trove/internal/staleness"
	"trove/internal/store"
	"trove/web"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	store *store.Store
	log   *slog.Logger
	mux   *http.ServeMux

	// stalenessInterval is how often the background ticker re-evaluates agent
	// heartbeats and flags stale services.
	stalenessInterval time.Duration
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
	}
	s.routes()
	return s
}

// Handler returns the HTTP handler for the whole server.
func (s *Server) Handler() http.Handler {
	return withRecover(s.log, s.mux)
}

func (s *Server) routes() {
	// Agent ingest — the only authenticated endpoint.
	s.mux.HandleFunc("POST /api/v1/report", s.requireAgent(s.handleReport))

	// Read-only dashboard APIs — no auth in Phase 1 (see README: bind to a
	// trusted network).
	s.mux.HandleFunc("GET /api/v1/services", s.handleServices)
	s.mux.HandleFunc("GET /api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Embedded SPA. FileServerFS serves index.html for "/" and 404s cleanly
	// for missing assets.
	s.mux.Handle("GET /", http.FileServerFS(web.FS()))
}

// RunStalenessLoop runs the background ticker until ctx is cancelled. It marks
// services belonging to stale/offline agents as health="stale". It evaluates
// once immediately so a freshly started server converges quickly.
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
	var staleIDs []int64
	for _, a := range agents {
		var lastSeen *time.Time
		if a.LastSeenAt.Valid {
			t := time.Unix(a.LastSeenAt.Int64, 0).UTC()
			lastSeen = &t
		}
		if staleness.StaleOrWorse(staleness.Evaluate(lastSeen, a.IntervalSeconds, now)) {
			staleIDs = append(staleIDs, a.ID)
		}
	}
	n, err := s.store.MarkServicesStaleForAgents(ctx, staleIDs)
	if err != nil {
		s.log.Error("staleness: mark services", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("staleness: flagged services stale", "count", n, "agents", len(staleIDs))
	}
}
