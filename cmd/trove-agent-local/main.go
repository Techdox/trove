// Command trove-agent-local discovers systemd service units on a Linux host and
// pushes full-state reports to a Trove server. It is read-only: it only runs
// `systemctl list-units` (a query) and never starts/stops/modifies units.
//
// Config (environment):
//
//	TROVE_SERVER_URL / TROVE_TOKEN / TROVE_INTERVAL / TROVE_AGENT_NAME  (common)
//	TROVE_LOCAL_UNIT_FILTER  glob to select units (e.g. "docker*"); default: all
//	TROVE_LOCAL_ALL          "true" to include inactive units too (default: only
//	                         active/activating/failed units, to cut noise)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"trove/internal/agentkit"
	"trove/pkg/model"
)

const version = "0.2.0"

// systemdUnit mirrors an entry from `systemctl list-units --output=json`.
type systemdUnit struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"` // active | inactive | failed | activating | deactivating
	Sub         string `json:"sub"`    // running | exited | dead | failed | ...
	Description string `json:"description"`
}

type collector struct {
	log        *slog.Logger
	filter     string // optional glob
	includeAll bool   // include inactive units
	hostname   string
}

func (c *collector) Collect(ctx context.Context) ([]agentkit.HostSnapshot, error) {
	out, err := exec.CommandContext(ctx, "systemctl",
		"list-units", "--type=service", "--all", "--output=json", "--no-pager", "--no-legend").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var units []systemdUnit
	if err := json.Unmarshal(out, &units); err != nil {
		return nil, fmt.Errorf("parse systemctl json: %w", err)
	}

	services := make([]model.ReportService, 0, len(units))
	for _, u := range units {
		if !strings.HasSuffix(u.Unit, ".service") {
			continue
		}
		if !c.includeAll && !isInteresting(u.Active) {
			continue
		}
		if c.filter != "" {
			if ok, _ := path.Match(c.filter, u.Unit); !ok {
				continue
			}
		}
		services = append(services, model.ReportService{
			ExternalID: u.Unit,
			Name:       strings.TrimSuffix(u.Unit, ".service"),
			Kind:       model.KindProcess,
			State:      u.Sub,
			Health:     mapUnitHealth(u.Active, u.Sub),
			Labels: map[string]string{
				"active":      u.Active,
				"load":        u.Load,
				"description": u.Description,
			},
		})
	}

	return []agentkit.HostSnapshot{{
		Host:     model.ReportHost{Hostname: c.hostname, Meta: map[string]string{"platform": "local"}},
		Services: services,
	}}, nil
}

// isInteresting keeps running/failed units by default and drops inactive noise.
func isInteresting(active string) bool {
	switch active {
	case "active", "activating", "failed", "reloading":
		return true
	default:
		return false
	}
}

// mapUnitHealth normalizes systemd state. systemd has no app-level healthcheck,
// so only a failed unit is clearly unhealthy; running units are "unknown" (the
// state badge carries the up/down signal).
func mapUnitHealth(active, sub string) model.Health {
	if active == "failed" || sub == "failed" {
		return model.HealthUnhealthy
	}
	return model.HealthUnknown
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := agentkit.LoadConfig()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}

	hostname := cfg.AgentName
	includeAll, _ := parseBool(os.Getenv("TROVE_LOCAL_ALL"))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	col := &collector{
		log:        log,
		filter:     os.Getenv("TROVE_LOCAL_UNIT_FILTER"),
		includeAll: includeAll,
		hostname:   hostname,
	}
	agentkit.Run(ctx, cfg, "local", version, col, log)
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, true
	default:
		return false, false
	}
}
