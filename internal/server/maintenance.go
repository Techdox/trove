package server

import (
	"context"
	"os"
	"time"
)

// RetentionConfig controls how long history is kept (ROADMAP decision D1).
type RetentionConfig struct {
	// Events: how long state/health/agent events are kept.
	Events time.Duration
	// Removed: how long soft-removed services linger before hard deletion.
	Removed time.Duration
}

const (
	defaultEventRetention   = 30 * 24 * time.Hour // 30 days
	defaultRemovedRetention = 24 * time.Hour
	maintenanceInterval     = time.Hour
)

// LoadRetentionConfigFromEnv reads TROVE_EVENT_RETENTION and
// TROVE_REMOVED_RETENTION (Go durations, e.g. "720h"), with safe defaults.
func LoadRetentionConfigFromEnv() RetentionConfig {
	cfg := RetentionConfig{Events: defaultEventRetention, Removed: defaultRemovedRetention}
	if v := os.Getenv("TROVE_EVENT_RETENTION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Events = d
		}
	}
	if v := os.Getenv("TROVE_REMOVED_RETENTION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Removed = d
		}
	}
	return cfg
}

// ConfigureRetention sets the retention windows used by the maintenance loop.
func (s *Server) ConfigureRetention(cfg RetentionConfig) {
	s.retention = cfg
}

// RunMaintenanceLoop prunes old events and long-removed services on an hourly
// ticker (plus once at startup). Pruning is deliberately off the report-ingest
// write path so heavy fleets don't pay for it on every push.
func (s *Server) RunMaintenanceLoop(ctx context.Context) {
	// Each window is guarded independently: a zero/unset Removed must not
	// mean "prune removed services immediately on every pass."
	if s.retention.Events <= 0 {
		s.retention.Events = defaultEventRetention
	}
	if s.retention.Removed <= 0 {
		s.retention.Removed = defaultRemovedRetention
	}
	t := time.NewTicker(maintenanceInterval)
	defer t.Stop()
	s.runMaintenance(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runMaintenance(ctx)
		}
	}
}

func (s *Server) runMaintenance(ctx context.Context) {
	stats, err := s.store.Prune(ctx,
		int64(s.retention.Events.Seconds()), int64(s.retention.Removed.Seconds()))
	if err != nil {
		s.log.Error("maintenance: prune", "err", err)
		return
	}
	if stats.Events > 0 || stats.RemovedServices > 0 {
		s.log.Info("maintenance: pruned",
			"events", stats.Events, "removed_services", stats.RemovedServices)
	}
}
