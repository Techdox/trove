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
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/techdox/trove/internal/agentkit"
	"github.com/techdox/trove/internal/hostmetrics"
	"github.com/techdox/trove/pkg/model"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := agentkit.LoadConfig()
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

	// Verify the daemon is reachable, but don't die if it's briefly
	// unavailable at startup — the loop will retry.
	if err := cli.ping(ctx); err != nil {
		log.Warn("docker daemon not reachable yet; will retry", "err", err)
	}

	var metrics metricSampler
	if cli.localHost {
		metrics = hostmetrics.NewLinuxSampler(false)
	}
	col := &collector{cli: cli, log: log, metrics: metrics}
	agentkit.Run(ctx, cfg, model.PlatformDocker, version, col, log)
}
