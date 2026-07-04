package alert

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/techdox/trove/internal/store"
)

func TestLoadDigestConfigFromEnvInvalidScheduleDisablesAndLogs(t *testing.T) {
	t.Setenv("TROVE_SMTP_HOST", "smtp.example.com")
	t.Setenv("TROVE_SMTP_FROM", "trove@example.com")
	t.Setenv("TROVE_SMTP_TO", "me@example.com")
	t.Setenv("TROVE_DIGEST", "whenever@possible")

	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := LoadDigestConfigFromEnv(log)

	if cfg.Enabled() {
		t.Fatal("an invalid schedule must disable the digest, not silently pick a default")
	}
	if !strings.Contains(buf.String(), "invalid TROVE_DIGEST") {
		t.Fatalf("expected a warning about the invalid schedule, got log: %s", buf.String())
	}
}

func TestBuildMIMEHeadersAndUniqueBoundary(t *testing.T) {
	cfg := DigestConfig{From: "trove@example.com", To: []string{"a@example.com", "b@example.com"}}
	m1 := string(buildMIME(cfg, "Subject One", "plain body", "<p>html body</p>"))
	m2 := string(buildMIME(cfg, "Subject One", "plain body", "<p>html body</p>"))

	for _, want := range []string{
		"From: trove@example.com\r\n",
		"To: a@example.com, b@example.com\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: multipart/alternative; boundary=",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Type: text/html; charset=utf-8\r\n",
		"Content-Transfer-Encoding: 8bit\r\n",
		"plain body",
		"<p>html body</p>",
	} {
		if !strings.Contains(m1, want) {
			t.Fatalf("MIME output missing %q\n---\n%s", want, m1)
		}
	}

	// Boundary must appear as both the opening and closing delimiter.
	i := strings.Index(m1, `boundary="`) + len(`boundary="`)
	boundary := m1[i : strings.Index(m1[i:], `"`)+i]
	if !strings.Contains(m1, "--"+boundary+"--\r\n") {
		t.Fatalf("closing boundary delimiter not found for %q", boundary)
	}

	// Two calls must not reuse the same boundary (defends against a body that
	// happens to contain a fixed boundary string).
	if m1 == m2 {
		t.Fatal("two digests with identical content should still get distinct boundaries")
	}
}

func TestDigesterTickCatchUpOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	sent := 0
	d := &Digester{
		store: st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:   DigestConfig{SMTPHost: "x", From: "a@example.com", To: []string{"b@example.com"}, Schedule: mustSchedule(t, "daily@08:00")},
		send:  func(DigestConfig, string, string, string) error { sent++; return nil },
	}
	clock := time.Date(2026, 1, 1, 7, 0, 0, 0, time.Local)
	d.now = func() time.Time { return clock }

	d.tick(ctx) // first boot: anchors, sends nothing
	if sent != 0 {
		t.Fatalf("first tick must only anchor the schedule, sent=%d", sent)
	}

	// Down for exactly one slot: crossing 08:00 once must send exactly once,
	// even if ticked repeatedly before the next slot.
	clock = clock.Add(90 * time.Minute) // now 08:30, one slot past
	d.tick(ctx)
	d.tick(ctx)
	d.tick(ctx)
	if sent != 1 {
		t.Fatalf("one missed slot must send exactly once, sent=%d", sent)
	}

	// Down for many days: still exactly one catch-up digest, not one per day.
	clock = clock.AddDate(0, 0, 5)
	d.tick(ctx)
	d.tick(ctx)
	if sent != 2 {
		t.Fatalf("multi-day gap must still send exactly one catch-up digest, sent=%d", sent)
	}
}

func TestDigesterTickCorruptLastSentReanchorsInsteadOfEpoch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	sent := 0
	d := &Digester{
		store: st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:   DigestConfig{SMTPHost: "x", From: "a@example.com", To: []string{"b@example.com"}, Schedule: mustSchedule(t, "daily@08:00")},
		send:  func(DigestConfig, string, string, string) error { sent++; return nil },
	}
	clock := time.Date(2026, 1, 1, 7, 0, 0, 0, time.Local)
	d.now = func() time.Time { return clock }

	if err := st.SetMeta(ctx, digestLastSentKey, "not-a-timestamp"); err != nil {
		t.Fatalf("seed corrupt meta: %v", err)
	}
	d.tick(ctx)
	if sent != 0 {
		t.Fatalf("a corrupt last-sent value must re-anchor, not trigger an immediate send, sent=%d", sent)
	}
	raw, ok, _ := st.GetMeta(ctx, digestLastSentKey)
	if !ok {
		t.Fatal("expected last-sent to be re-anchored")
	}
	if v, _ := strconv.ParseInt(raw, 10, 64); v != clock.Unix() {
		t.Fatalf("expected re-anchor to now (%d), got %d", clock.Unix(), v)
	}
}

func mustSchedule(t *testing.T, s string) Schedule {
	t.Helper()
	sched, err := ParseSchedule(s)
	if err != nil {
		t.Fatalf("parse schedule %q: %v", s, err)
	}
	return sched
}
