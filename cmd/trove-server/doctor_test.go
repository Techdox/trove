package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/techdox/trove/internal/store"
)

func TestRunDoctorReportsReadOnlyHealthyStateWithoutSecrets(t *testing.T) {
	path := newDoctorDatabase(t)
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)
	t.Setenv("TROVE_ADDR", "127.0.0.1:8080")
	t.Setenv("TROVE_OIDC_ISSUER", "https://idp.example")
	t.Setenv("TROVE_OIDC_CLIENT_ID", "doctor-client")
	t.Setenv("TROVE_OIDC_CLIENT_SECRET", "doctor-client-secret-must-not-appear")
	t.Setenv("TROVE_OIDC_REDIRECT_URL", "https://trove.example/oauth2/callback")
	t.Setenv("TROVE_API_TOKEN", "doctor-api-token-must-not-appear-123456")

	output, err := captureDoctorOutput(t, runDoctor)
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	for _, want := range []string{"Trove doctor", "database: ok (read-only integrity check)", "migrations: current", "configuration: valid", "result: ok"} {
		if !strings.Contains(output, want) {
			t.Errorf("doctor output missing %q:\n%s", want, output)
		}
	}
	for _, secret := range []string{"doctor-client-secret-must-not-appear", "doctor-api-token-must-not-appear-123456"} {
		if strings.Contains(output, secret) {
			t.Errorf("doctor output exposed a secret: %q", secret)
		}
	}
}

func TestRunDoctorAcceptsRetiredMigrationHistory(t *testing.T) {
	path := newDoctorDatabase(t)
	addDoctorMigration(t, path, "0008_runtime_settings.sql")
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)

	output, err := captureDoctorOutput(t, runDoctor)
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	if !strings.Contains(output, "1 retired") {
		t.Errorf("doctor output did not report retired migration:\n%s", output)
	}
	if !strings.Contains(output, "result: ok") {
		t.Errorf("doctor output did not report success:\n%s", output)
	}
}

func TestRunDoctorRejectsUnknownMigrationHistory(t *testing.T) {
	path := newDoctorDatabase(t)
	addDoctorMigration(t, path, "9999_unknown.sql")
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)

	output, err := captureDoctorOutput(t, runDoctor)
	if err == nil {
		t.Fatal("doctor accepted an unknown migration")
	}
	if !strings.Contains(output, "migrations: not current") || !strings.Contains(output, "1 unknown") {
		t.Errorf("doctor output did not report unknown migration:\n%s", output)
	}
}

func TestRunDoctorReportsInvalidConfiguration(t *testing.T) {
	path := newDoctorDatabase(t)
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)
	t.Setenv("TROVE_ADDR", "not-an-address")

	output, err := captureDoctorOutput(t, runDoctor)
	if err == nil {
		t.Fatal("doctor accepted an invalid listen address")
	}
	if !strings.Contains(output, "TROVE_ADDR must be a valid host:port address") {
		t.Errorf("doctor output did not explain invalid address:\n%s", output)
	}
}

func TestRunDoctorReportsPartialBootstrapConfiguration(t *testing.T) {
	path := newDoctorDatabase(t)
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)
	t.Setenv("TROVE_BOOTSTRAP_AGENT", "quickstart-agent")

	output, err := captureDoctorOutput(t, runDoctor)
	if err == nil {
		t.Fatal("doctor accepted a partial bootstrap configuration")
	}
	if !strings.Contains(output, "TROVE_BOOTSTRAP_AGENT and TROVE_BOOTSTRAP_TOKEN must both be set") {
		t.Errorf("doctor output did not explain partial bootstrap configuration:\n%s", output)
	}
}

func TestRunDoctorReportsDisabledDigest(t *testing.T) {
	path := newDoctorDatabase(t)
	clearDoctorEnv(t)
	t.Setenv("TROVE_DB", path)
	t.Setenv("TROVE_SMTP_HOST", "smtp.example")
	t.Setenv("TROVE_SMTP_FROM", "trove@example")
	t.Setenv("TROVE_SMTP_TO", "operator@example")
	t.Setenv("TROVE_DIGEST", "off")

	output, err := captureDoctorOutput(t, runDoctor)
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	if !strings.Contains(output, "digest=disabled") {
		t.Errorf("doctor output did not report disabled digest:\n%s", output)
	}
}

func newDoctorDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trove.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open doctor database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close doctor database: %v", err)
	}
	return path
}

func addDoctorMigration(t *testing.T, path, version string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open doctor database: %v", err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		version,
		time.Now().Unix(),
	); err != nil {
		_ = st.Close()
		t.Fatalf("insert doctor migration: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close doctor database: %v", err)
	}
}

func clearDoctorEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"TROVE_ADDR", "TROVE_DB", "TROVE_BOOTSTRAP_AGENT", "TROVE_BOOTSTRAP_TOKEN", "TROVE_OIDC_ISSUER", "TROVE_OIDC_CLIENT_ID", "TROVE_OIDC_CLIENT_SECRET", "TROVE_OIDC_REDIRECT_URL", "TROVE_API_TOKEN", "TROVE_OIDC_SESSION_MAX_AGE", "TROVE_HEALTH_DETAILS_ENABLED", "TROVE_FRESHNESS_ENABLED", "TROVE_FRESHNESS_INTERVAL", "TROVE_FRESHNESS_TTL", "TROVE_EVENT_RETENTION", "TROVE_REMOVED_RETENTION", "TROVE_HOST_RETENTION", "TROVE_ALERT_COOLDOWN", "TROVE_REGISTRY_AUTHS", "TROVE_SMTP_PORT", "TROVE_DIGEST", "TROVE_ALERT_EVENTS", "TROVE_ALERT_WEBHOOK_URL", "TROVE_ALERT_DISCORD_URL", "TROVE_ALERT_NTFY_URL", "TROVE_SMTP_HOST", "TROVE_SMTP_FROM", "TROVE_SMTP_TO",
	} {
		t.Setenv(key, "")
	}
}

func captureDoctorOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previous
		_ = reader.Close()
		_ = writer.Close()
	})

	callErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return string(output), callErr
}
