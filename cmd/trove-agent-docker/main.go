// Command trove-agent-docker discovers containers on a Docker host and pushes
// full-state reports to a Trove server on an interval. It is strictly
// read-only: it talks to the Docker Engine API via GET requests only and never
// mutates container or daemon state.
//
// Config (all via environment):
//
//	TROVE_SERVER_URL   base URL of the trove-server (required), e.g. http://trove:8080
//	TROVE_TOKEN        bearer token minted by `trove-server agent create` (required)
//	TROVE_INTERVAL     push interval, Go duration or seconds (default 30s)
//	TROVE_AGENT_NAME   name reported to the server (default: hostname)
//	DOCKER_HOST        Docker endpoint (default unix:///var/run/docker.sock)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"trove/pkg/model"
)

const version = "0.1.0"

type config struct {
	serverURL string
	token     string
	interval  time.Duration
	agentName string
}

func loadConfig() (config, error) {
	var c config
	c.serverURL = strings.TrimRight(os.Getenv("TROVE_SERVER_URL"), "/")
	if c.serverURL == "" {
		return c, fmt.Errorf("TROVE_SERVER_URL is required")
	}
	c.token = os.Getenv("TROVE_TOKEN")
	if c.token == "" {
		return c, fmt.Errorf("TROVE_TOKEN is required")
	}
	c.interval = parseInterval(os.Getenv("TROVE_INTERVAL"))
	c.agentName = os.Getenv("TROVE_AGENT_NAME")
	if c.agentName == "" {
		h, _ := os.Hostname()
		if h == "" {
			h = "docker-agent"
		}
		c.agentName = h
	}
	return c, nil
}

// parseInterval accepts a Go duration ("30s", "1m") or a bare integer count of
// seconds ("30"), falling back to the model default.
func parseInterval(s string) time.Duration {
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

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	cli, err := newDockerClient(os.Getenv("DOCKER_HOST"))
	if err != nil {
		log.Error("docker client", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Verify the daemon is reachable before entering the loop, but don't die if
	// it's briefly unavailable at startup — just warn and let the loop retry.
	if err := cli.ping(ctx); err != nil {
		log.Warn("docker daemon not reachable yet; will retry", "err", err)
	}

	col := &collector{
		cli:             cli,
		log:             log,
		agentName:       cfg.agentName,
		agentVersion:    version,
		intervalSeconds: int(cfg.interval.Seconds()),
	}
	pusher := &pusher{
		http:      &http.Client{Timeout: 15 * time.Second},
		serverURL: cfg.serverURL,
		token:     cfg.token,
	}

	log.Info("trove-agent-docker starting",
		"server", cfg.serverURL, "agent", cfg.agentName, "interval", cfg.interval)

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	runOnce(ctx, log, col, pusher) // push immediately, don't wait a full interval
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-ticker.C:
			runOnce(ctx, log, col, pusher)
		}
	}
}

func runOnce(ctx context.Context, log *slog.Logger, col *collector, p *pusher) {
	report, err := col.Collect(ctx)
	if err != nil {
		log.Error("collect failed", "err", err)
		return
	}
	if err := p.push(ctx, report); err != nil {
		log.Error("push failed", "err", err)
		return
	}
	log.Info("report pushed", "host", report.Host.Hostname, "services", len(report.Services))
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
