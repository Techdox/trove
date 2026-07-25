package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/internal/alert"
	"github.com/techdox/trove/internal/registry"
	"github.com/techdox/trove/internal/server"
	"github.com/techdox/trove/internal/store"
)

// runDoctor inspects local configuration and the configured SQLite database.
// It deliberately opens the database read-only: it never creates a database,
// applies migrations, contacts external services, or prints secret values.
func runDoctor() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("Trove doctor")
	fmt.Printf("binary: %s (%s %s)\n", version, runtime.GOOS, runtime.GOARCH)

	st, err := store.OpenReadOnly(envOr("TROVE_DB", "trove.db"))
	if err != nil {
		return fmt.Errorf("open configured database read-only: %w", err)
	}
	defer st.Close()

	problems := make([]string, 0)
	if integrity, err := st.CheckIntegrity(ctx); err != nil {
		problems = append(problems, err.Error())
		fmt.Println("database: unavailable")
	} else if integrity != "ok" {
		problems = append(problems, "SQLite integrity check did not return ok")
		fmt.Printf("database: integrity check failed (%s)\n", integrity)
	} else {
		fmt.Println("database: ok (read-only integrity check)")
	}

	if migrations, err := st.MigrationStatus(ctx); err != nil {
		problems = append(problems, err.Error())
		fmt.Println("migrations: unavailable")
	} else if len(migrations.Pending) != 0 || len(migrations.Unknown) != 0 {
		if len(migrations.Pending) != 0 {
			problems = append(problems, "database has pending migrations")
		}
		if len(migrations.Unknown) != 0 {
			problems = append(problems, "database has migrations unknown to this binary")
		}
		fmt.Printf("migrations: not current (%d/%d applied)\n", len(migrations.Applied), len(migrations.Applied)+len(migrations.Pending))
	} else {
		fmt.Printf("migrations: current (%d applied)\n", len(migrations.Applied))
	}

	configProblems := validateDoctorConfig()
	if len(configProblems) == 0 {
		fmt.Println("configuration: valid (secrets redacted)")
	} else {
		problems = append(problems, configProblems...)
		fmt.Println("configuration: invalid")
		for _, problem := range configProblems {
			fmt.Printf("  - %s\n", problem)
		}
	}
	fmt.Printf("workers: freshness=%s, alerts=%s, digest=%s\n", doctorFreshnessStatus(), doctorAlertsStatus(), doctorDigestStatus())

	if len(problems) != 0 {
		return fmt.Errorf("doctor found %d problem(s)", len(problems))
	}
	fmt.Println("result: ok")
	return nil
}

func validateDoctorConfig() []string {
	problems := make([]string, 0)
	if addr := envOr("TROVE_ADDR", ":8080"); !validListenAddress(addr) {
		problems = append(problems, "TROVE_ADDR must be a valid host:port address")
	}
	if err := server.LoadOIDCConfigFromEnv().Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	if raw := os.Getenv("TROVE_OIDC_SESSION_MAX_AGE"); raw != "" && !validPositiveDuration(raw) {
		problems = append(problems, "TROVE_OIDC_SESSION_MAX_AGE must be a positive duration")
	}
	if raw := os.Getenv("TROVE_HEALTH_DETAILS_ENABLED"); raw != "" && !validBool(raw) {
		problems = append(problems, "TROVE_HEALTH_DETAILS_ENABLED must be true or false")
	}
	if raw := os.Getenv("TROVE_FRESHNESS_ENABLED"); raw != "" && !validBool(raw) {
		problems = append(problems, "TROVE_FRESHNESS_ENABLED must be true or false")
	}
	for _, key := range []string{"TROVE_FRESHNESS_INTERVAL", "TROVE_FRESHNESS_TTL", "TROVE_EVENT_RETENTION", "TROVE_REMOVED_RETENTION", "TROVE_HOST_RETENTION"} {
		if raw := os.Getenv(key); raw != "" && !validPositiveDuration(raw) {
			problems = append(problems, key+" must be a positive duration")
		}
	}
	if raw := os.Getenv("TROVE_ALERT_COOLDOWN"); raw != "" && !validNonNegativeDuration(raw) {
		problems = append(problems, "TROVE_ALERT_COOLDOWN must be a non-negative duration")
	}
	if raw := os.Getenv("TROVE_REGISTRY_AUTHS"); raw != "" {
		var creds map[string]registry.Cred
		if !json.Valid([]byte(raw)) || json.Unmarshal([]byte(raw), &creds) != nil {
			problems = append(problems, "TROVE_REGISTRY_AUTHS must contain a valid registry credential map")
		}
	}
	if raw := os.Getenv("TROVE_SMTP_PORT"); raw != "" && !validPort(raw) {
		problems = append(problems, "TROVE_SMTP_PORT must be a port number between 1 and 65535")
	}
	if raw := os.Getenv("TROVE_DIGEST"); raw != "" {
		if _, err := alert.ParseSchedule(raw); err != nil {
			problems = append(problems, "TROVE_DIGEST must be off, daily@HH:MM, or weekly@day:HH:MM")
		}
	}
	if raw := os.Getenv("TROVE_ALERT_EVENTS"); raw != "" {
		for _, kind := range strings.Split(raw, ",") {
			kind = strings.TrimSpace(strings.ToLower(kind))
			if kind != "" && !validAlertKind(kind) {
				problems = append(problems, "TROVE_ALERT_EVENTS contains an unknown event kind")
				break
			}
		}
	}
	return problems
}

func validAlertKind(kind string) bool {
	switch kind {
	case "agent", "freshness", "health", "host", "state":
		return true
	default:
		return false
	}
}

func validListenAddress(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	return err == nil && validPort(port)
}

func validPort(raw string) bool {
	port, err := strconv.ParseUint(raw, 10, 16)
	return err == nil && port > 0
}

func validBool(raw string) bool {
	_, err := strconv.ParseBool(raw)
	return err == nil
}

func validPositiveDuration(raw string) bool {
	d, err := time.ParseDuration(raw)
	return err == nil && d > 0
}

func validNonNegativeDuration(raw string) bool {
	d, err := time.ParseDuration(raw)
	return err == nil && d >= 0
}

func doctorFreshnessStatus() string {
	if raw := os.Getenv("TROVE_FRESHNESS_ENABLED"); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil && !enabled {
			return "disabled"
		}
	}
	return "enabled"
}

func doctorAlertsStatus() string {
	for _, key := range []string{"TROVE_ALERT_WEBHOOK_URL", "TROVE_ALERT_DISCORD_URL", "TROVE_ALERT_NTFY_URL"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return "configured"
		}
	}
	return "disabled"
}

func doctorDigestStatus() string {
	if strings.TrimSpace(os.Getenv("TROVE_SMTP_HOST")) != "" &&
		strings.TrimSpace(os.Getenv("TROVE_SMTP_FROM")) != "" &&
		strings.TrimSpace(os.Getenv("TROVE_SMTP_TO")) != "" {
		return "configured"
	}
	return "disabled"
}
