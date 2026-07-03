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
//	TROVE_PROXMOX_INSECURE  "true" to skip TLS verification (self-signed certs)
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"trove/internal/agentkit"
)

const version = "0.2.0"

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
	agentkit.Run(ctx, cfg, "proxmox", version, col, log)
}
