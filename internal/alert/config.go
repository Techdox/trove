// Package alert turns Trove's event stream into outbound notifications:
// instant pushes (generic webhook, Discord, ntfy) driven by a cursor over the
// events table, plus a scheduled email digest. Sending a notification is the
// only thing this package does to the outside world — it mutates nothing.
package alert

import (
	"os"
	"strings"
	"time"
)

// Config is the alerting configuration, loaded from environment variables.
type Config struct {
	// Channels; empty URL = channel disabled.
	WebhookURL    string
	WebhookSecret string
	DiscordURL    string
	NtfyURL       string
	NtfyToken     string

	// Kinds enabled for instant notifications: state, health, agent, freshness.
	Kinds map[string]bool

	// Cooldown is the minimum interval between notifications for the same key
	// (flap suppression). Escalations to critical bypass it once.
	Cooldown time.Duration

	// Interval between engine sweeps.
	Interval time.Duration
}

const (
	defaultCooldown = 5 * time.Minute
	defaultInterval = 30 * time.Second
)

// LoadConfigFromEnv reads:
//
//	TROVE_ALERT_WEBHOOK_URL     generic JSON webhook
//	TROVE_ALERT_WEBHOOK_SECRET  optional HMAC-SHA256 signing secret for the generic webhook
//	TROVE_ALERT_DISCORD_URL     Discord webhook
//	TROVE_ALERT_NTFY_URL      full ntfy topic URL (e.g. https://ntfy.sh/my-trove)
//	TROVE_ALERT_NTFY_TOKEN    optional ntfy access token
//	TROVE_ALERT_EVENTS        comma list of kinds (default "agent,health,state,freshness")
//	TROVE_ALERT_COOLDOWN      per-key flap suppression window (default 5m)
func LoadConfigFromEnv() Config {
	cfg := Config{
		WebhookURL:    os.Getenv("TROVE_ALERT_WEBHOOK_URL"),
		WebhookSecret: os.Getenv("TROVE_ALERT_WEBHOOK_SECRET"),
		DiscordURL:    os.Getenv("TROVE_ALERT_DISCORD_URL"),
		NtfyURL:       os.Getenv("TROVE_ALERT_NTFY_URL"),
		NtfyToken:     os.Getenv("TROVE_ALERT_NTFY_TOKEN"),
		Kinds:         map[string]bool{},
		Cooldown:      defaultCooldown,
		Interval:      defaultInterval,
	}
	kinds := os.Getenv("TROVE_ALERT_EVENTS")
	if kinds == "" {
		kinds = "agent,health,state,freshness"
	}
	for _, k := range strings.Split(kinds, ",") {
		if k = strings.TrimSpace(strings.ToLower(k)); k != "" {
			cfg.Kinds[k] = true
		}
	}
	if v := os.Getenv("TROVE_ALERT_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.Cooldown = d
		}
	}
	return cfg
}

// Enabled reports whether any instant channel is configured.
func (c Config) Enabled() bool {
	return c.WebhookURL != "" || c.DiscordURL != "" || c.NtfyURL != ""
}
