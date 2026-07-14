package server

import (
	"testing"
	"time"
)

func TestLoadRetentionConfigFromEnvIncludesHosts(t *testing.T) {
	t.Setenv("TROVE_EVENT_RETENTION", "48h")
	t.Setenv("TROVE_REMOVED_RETENTION", "12h")
	t.Setenv("TROVE_HOST_RETENTION", "168h")

	cfg := LoadRetentionConfigFromEnv()
	if cfg.Events != 48*time.Hour {
		t.Errorf("Events = %v, want 48h", cfg.Events)
	}
	if cfg.Removed != 12*time.Hour {
		t.Errorf("Removed = %v, want 12h", cfg.Removed)
	}
	if cfg.Hosts != 168*time.Hour {
		t.Errorf("Hosts = %v, want 168h", cfg.Hosts)
	}
}

func TestLoadRetentionConfigFromEnvUsesSafeHostDefault(t *testing.T) {
	t.Setenv("TROVE_EVENT_RETENTION", "")
	t.Setenv("TROVE_REMOVED_RETENTION", "")
	t.Setenv("TROVE_HOST_RETENTION", "invalid")

	cfg := LoadRetentionConfigFromEnv()
	if cfg.Hosts != defaultHostRetention {
		t.Errorf("Hosts = %v, want default %v", cfg.Hosts, defaultHostRetention)
	}
}
