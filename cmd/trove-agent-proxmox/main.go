// Command trove-agent-proxmox discovers VMs and LXC containers across a Proxmox
// VE cluster and pushes full-state reports (one per node) to a Trove server. It
// is read-only: it uses an API token and only issues GETs.
//
// Config (environment):
//
//	TROVE_SERVER_URL     base URL of the trove-server (required)
//	TROVE_TOKEN          Trove bearer token (required)
//	TROVE_INTERVAL       push interval (default 30s)
//	TROVE_AGENT_NAME     name reported to the server (default: hostname)
//	TROVE_PROXMOX_URL    Proxmox API base, e.g. https://pve.example:8006 (required)
//	TROVE_PROXMOX_TOKEN  Proxmox API token: USER@REALM!TOKENID=SECRET (required)
//	TROVE_PROXMOX_CA_FILE  PEM CA bundle for private/self-signed PVE certificates
//	TROVE_PROXMOX_INSECURE  "true" to skip TLS verification (self-signed certs)
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/techdox/trove/internal/agentkit"
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
	pcfg, err := loadProxmoxConfig()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	col := &collector{cli: newProxmoxClient(pcfg), log: log}
	agentkit.Run(ctx, cfg, model.PlatformProxmox, version, col, log)
}
