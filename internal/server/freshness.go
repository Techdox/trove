package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/techdox/trove/internal/registry"
)

// FreshnessConfig controls the image-freshness checker.
type FreshnessConfig struct {
	Enabled  bool
	Interval time.Duration // how often to scan for images due a check
	TTL      time.Duration // how long a successful result is considered current
	Creds    map[string]registry.Cred
}

// freshness tuning constants.
const (
	defaultFreshnessInterval = 5 * time.Minute
	defaultFreshnessTTL      = 6 * time.Hour
	freshnessErrorBackoff    = 30 * time.Minute
	freshnessRateBackoff     = 2 * time.Hour
	freshnessBatch           = 50 // images resolved per scan
	freshnessConcurrency     = 4  // concurrent registry lookups
)

// LoadFreshnessConfigFromEnv builds the checker config from environment:
//
//	TROVE_FRESHNESS_ENABLED    "false"/"0" to disable (default enabled)
//	TROVE_FRESHNESS_INTERVAL   scan cadence (Go duration, default 5m)
//	TROVE_FRESHNESS_TTL        per-image recheck interval (default 6h)
//	TROVE_REGISTRY_AUTHS       JSON {"host":{"username":..,"password":..}}
func LoadFreshnessConfigFromEnv() FreshnessConfig {
	cfg := FreshnessConfig{
		Enabled:  true,
		Interval: defaultFreshnessInterval,
		TTL:      defaultFreshnessTTL,
	}
	if v := os.Getenv("TROVE_FRESHNESS_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Enabled = b
		}
	}
	if v := os.Getenv("TROVE_FRESHNESS_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("TROVE_FRESHNESS_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.TTL = d
		}
	}
	if v := os.Getenv("TROVE_REGISTRY_AUTHS"); v != "" {
		var creds map[string]registry.Cred
		if err := json.Unmarshal([]byte(v), &creds); err == nil {
			cfg.Creds = creds
		}
	}
	return cfg
}

// ConfigureFreshness enables the freshness checker with the given config.
func (s *Server) ConfigureFreshness(cfg FreshnessConfig) {
	s.freshness = cfg
	s.registry = registry.New(cfg.Creds)
}

// RunFreshnessLoop periodically resolves the latest registry digest for images
// in use and caches the results. No-op (returns immediately) if disabled.
func (s *Server) RunFreshnessLoop(ctx context.Context) {
	if !s.freshness.Enabled || s.registry == nil {
		s.log.Info("image freshness checking disabled")
		return
	}
	t := time.NewTicker(s.freshness.Interval)
	defer t.Stop()
	s.evaluateFreshness(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.evaluateFreshness(ctx)
		}
	}
}

func (s *Server) evaluateFreshness(ctx context.Context) {
	images, err := s.store.ImagesDueForCheck(ctx, freshnessBatch)
	if err != nil {
		s.log.Error("freshness: images due", "err", err)
		return
	}
	if len(images) == 0 {
		return
	}
	s.log.Info("freshness: checking images", "count", len(images))

	sem := make(chan struct{}, freshnessConcurrency)
	var wg sync.WaitGroup
	for _, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func(img string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.checkImage(ctx, img)
		}(img)
	}
	wg.Wait()
}

func (s *Server) checkImage(ctx context.Context, image string) {
	digest, err := s.registry.LatestDigest(ctx, image)
	now := time.Now().UTC()
	switch {
	case errors.Is(err, registry.ErrRateLimited):
		_ = s.store.RecordImageError(ctx, image, "rate limited", now.Add(freshnessRateBackoff).Unix())
	case err != nil:
		s.log.Debug("freshness: check failed", "image", image, "err", err)
		_ = s.store.RecordImageError(ctx, image, err.Error(), now.Add(freshnessErrorBackoff).Unix())
	default:
		_ = s.store.RecordImageDigest(ctx, image, digest, now.Add(s.freshness.TTL).Unix())
	}
}
