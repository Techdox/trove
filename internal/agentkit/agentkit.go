// Package agentkit holds the machinery every Trove agent shares: common config
// loading, the report push client, and the collect-and-push loop. A concrete
// agent just implements Collector (platform discovery) and calls Run; the
// envelope, scheduling, and transport live here.
//
// Collector returns a slice of HostSnapshot so multi-host platforms (e.g. a
// Proxmox cluster with several nodes) can report each host independently while
// single-host platforms (Docker, one Kubernetes cluster) return exactly one.
package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/pkg/model"
)

// Config is the configuration common to all agents.
type Config struct {
	ServerURL string
	Token     string
	Interval  time.Duration
	AgentName string
}

// LoadConfig reads the common agent configuration from the environment:
//
//	TROVE_SERVER_URL   base URL of the trove-server (required)
//	TROVE_TOKEN        bearer token from `trove-server agent create` (required)
//	TROVE_INTERVAL     push interval, Go duration or bare seconds (default 30s)
//	TROVE_AGENT_NAME   name reported to the server (default: hostname)
func LoadConfig() (Config, error) {
	var c Config
	c.ServerURL = strings.TrimRight(os.Getenv("TROVE_SERVER_URL"), "/")
	if c.ServerURL == "" {
		return c, fmt.Errorf("TROVE_SERVER_URL is required")
	}
	c.Token = os.Getenv("TROVE_TOKEN")
	if c.Token == "" {
		return c, fmt.Errorf("TROVE_TOKEN is required")
	}
	c.Interval = ParseInterval(os.Getenv("TROVE_INTERVAL"))
	c.AgentName = os.Getenv("TROVE_AGENT_NAME")
	if c.AgentName == "" {
		h, _ := os.Hostname()
		if h == "" {
			h = "trove-agent"
		}
		c.AgentName = h
	}
	return c, nil
}

// ParseInterval accepts a Go duration ("30s", "1m") or a bare integer count of
// seconds ("30"), falling back to the model default.
func ParseInterval(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return model.DefaultReportInterval()
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return model.DefaultReportInterval()
}

// HostSnapshot is the discovered state of one host: its identity and its
// services.
type HostSnapshot struct {
	Host     model.ReportHost
	Services []model.ReportService
}

// Collector produces the current state for an agent, across one or more hosts.
type Collector interface {
	Collect(ctx context.Context) ([]HostSnapshot, error)
}

// Run drives the collect-and-push loop until ctx is cancelled. It pushes once
// immediately, then on every interval. Each HostSnapshot is sent as its own
// full-state report sharing the agent envelope built from cfg/platform/version.
func Run(ctx context.Context, cfg Config, platform, version string, c Collector, log *slog.Logger) {
	p := &pusher{
		http:      &http.Client{Timeout: 15 * time.Second},
		serverURL: cfg.ServerURL,
		token:     cfg.Token,
	}
	envelope := model.ReportAgent{
		Name:            cfg.AgentName,
		Platform:        platform,
		Version:         version,
		IntervalSeconds: int(cfg.Interval.Seconds()),
	}

	log.Info("agent starting", "server", cfg.ServerURL, "agent", cfg.AgentName,
		"platform", platform, "interval", cfg.Interval)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	runOnce(ctx, envelope, c, p, log)
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			runOnce(ctx, envelope, c, p, log)
		}
	}
}

func runOnce(ctx context.Context, envelope model.ReportAgent, c Collector, p *pusher, log *slog.Logger) {
	snaps, err := c.Collect(ctx)
	if err != nil {
		log.Error("collect failed", "err", err)
		return
	}
	total := 0
	for i := range snaps {
		report := &model.Report{Agent: envelope, Host: snaps[i].Host, Services: snaps[i].Services}
		if err := p.push(ctx, report); err != nil {
			log.Error("push failed", "host", snaps[i].Host.Hostname, "err", err)
			continue
		}
		total += len(snaps[i].Services)
	}
	log.Info("report pushed", "hosts", len(snaps), "services", total)
}

// pusher POSTs reports to the Trove server.
type pusher struct {
	http      *http.Client
	serverURL string
	token     string
}

func (p *pusher) push(ctx context.Context, report *model.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.serverURL+"/api/v1/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
