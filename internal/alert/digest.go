package alert

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/internal/store"
)

const digestLastSentKey = "digest_last_sent"

// DigestConfig is the scheduled-email configuration.
type DigestConfig struct {
	SMTPHost string
	SMTPPort int
	Username string
	Password string
	From     string
	To       []string
	Schedule Schedule
}

// LoadDigestConfigFromEnv reads:
//
//	TROVE_SMTP_HOST / TROVE_SMTP_PORT (default 587)
//	TROVE_SMTP_USERNAME / TROVE_SMTP_PASSWORD (optional)
//	TROVE_SMTP_FROM / TROVE_SMTP_TO (comma-separated)
//	TROVE_DIGEST  "daily@08:00" (default), "weekly@mon:08:00", or "off"
func LoadDigestConfigFromEnv() DigestConfig {
	cfg := DigestConfig{
		SMTPHost: os.Getenv("TROVE_SMTP_HOST"),
		SMTPPort: 587,
		Username: os.Getenv("TROVE_SMTP_USERNAME"),
		Password: os.Getenv("TROVE_SMTP_PASSWORD"),
		From:     os.Getenv("TROVE_SMTP_FROM"),
	}
	if v := os.Getenv("TROVE_SMTP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			cfg.SMTPPort = p
		}
	}
	for _, to := range strings.Split(os.Getenv("TROVE_SMTP_TO"), ",") {
		if to = strings.TrimSpace(to); to != "" {
			cfg.To = append(cfg.To, to)
		}
	}
	sched := os.Getenv("TROVE_DIGEST")
	if sched == "" {
		sched = "daily@08:00"
	}
	cfg.Schedule, _ = ParseSchedule(sched)
	return cfg
}

// Enabled reports whether the digest can and should run.
func (c DigestConfig) Enabled() bool {
	return c.SMTPHost != "" && c.From != "" && len(c.To) > 0 && !c.Schedule.Off
}

// Schedule is a parsed digest schedule.
type Schedule struct {
	Off     bool
	Weekday *time.Weekday // nil = daily
	Hour    int
	Minute  int
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseSchedule accepts "off", "daily@HH:MM", or "weekly@day:HH:MM".
func ParseSchedule(s string) (Schedule, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "off" || s == "none" || s == "false" {
		return Schedule{Off: true}, nil
	}
	mode, rest, ok := strings.Cut(s, "@")
	if !ok {
		return Schedule{Off: true}, fmt.Errorf("invalid digest schedule %q", s)
	}
	var sched Schedule
	switch mode {
	case "daily":
		// rest = HH:MM
	case "weekly":
		day, hm, ok := strings.Cut(rest, ":")
		if !ok {
			return Schedule{Off: true}, fmt.Errorf("invalid weekly schedule %q", s)
		}
		wd, found := weekdays[day]
		if !found {
			return Schedule{Off: true}, fmt.Errorf("invalid weekday %q", day)
		}
		sched.Weekday = &wd
		rest = hm
	default:
		return Schedule{Off: true}, fmt.Errorf("invalid digest schedule %q", s)
	}
	hh, mm, ok := strings.Cut(rest, ":")
	if !ok {
		return Schedule{Off: true}, fmt.Errorf("invalid time in schedule %q", s)
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return Schedule{Off: true}, fmt.Errorf("invalid time in schedule %q", s)
	}
	sched.Hour, sched.Minute = h, m
	return sched, nil
}

// NextAfter returns the first scheduled instant strictly after t (server-local
// time).
func (s Schedule) NextAfter(t time.Time) time.Time {
	t = t.Local()
	next := time.Date(t.Year(), t.Month(), t.Day(), s.Hour, s.Minute, 0, 0, t.Location())
	if s.Weekday == nil {
		if !next.After(t) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	}
	for next.Weekday() != *s.Weekday || !next.After(t) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Digester sends the scheduled summary email.
type Digester struct {
	store *store.Store
	log   *slog.Logger
	cfg   DigestConfig
	send  func(cfg DigestConfig, subject, text, htmlBody string) error
	now   func() time.Time
}

// NewDigester builds a digester using the SMTP sender.
func NewDigester(st *store.Store, log *slog.Logger, cfg DigestConfig) *Digester {
	return &Digester{
		store: st,
		log:   log,
		cfg:   cfg,
		send:  sendSMTP,
		now:   func() time.Time { return time.Now() },
	}
}

// Run checks once a minute whether a digest is due. A missed slot (server was
// down) is caught up once on the next check. No-op when not configured.
func (d *Digester) Run(ctx context.Context) {
	if !d.cfg.Enabled() {
		d.log.Info("email digest disabled")
		return
	}
	d.log.Info("email digest enabled", "to", strings.Join(d.cfg.To, ","))
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx)
		}
	}
}

func (d *Digester) tick(ctx context.Context) {
	raw, ok, err := d.store.GetMeta(ctx, digestLastSentKey)
	if err != nil {
		d.log.Error("digest: read last-sent", "err", err)
		return
	}
	now := d.now()
	if !ok {
		// First boot: anchor the schedule now; the first digest goes out at
		// the next scheduled slot, not immediately.
		_ = d.store.SetMeta(ctx, digestLastSentKey, strconv.FormatInt(now.Unix(), 10))
		return
	}
	lastSent, _ := strconv.ParseInt(raw, 10, 64)
	if now.Before(d.cfg.Schedule.NextAfter(time.Unix(lastSent, 0))) {
		return
	}
	if err := d.SendNow(ctx, time.Unix(lastSent, 0)); err != nil {
		d.log.Error("digest: send", "err", err)
		return // retry next minute; last-sent unchanged
	}
	_ = d.store.SetMeta(ctx, digestLastSentKey, strconv.FormatInt(now.Unix(), 10))
	d.log.Info("digest sent", "to", strings.Join(d.cfg.To, ","))
}

// SendNow builds and sends a digest covering everything since `since`.
func (d *Digester) SendNow(ctx context.Context, since time.Time) error {
	subject, text, htmlBody, err := d.build(ctx, since)
	if err != nil {
		return err
	}
	return d.send(d.cfg, subject, text, htmlBody)
}

// build assembles the digest content from current state + recent events.
func (d *Digester) build(ctx context.Context, since time.Time) (subject, text, htmlBody string, err error) {
	rows, err := d.store.ListServices(ctx)
	if err != nil {
		return "", "", "", err
	}
	agents, err := d.store.ListAgents(ctx)
	if err != nil {
		return "", "", "", err
	}
	events, err := d.store.RecentEvents(ctx, 500)
	if err != nil {
		return "", "", "", err
	}

	var total, running, unhealthyN int
	var unhealthy, outdated []string
	for i := range rows {
		r := &rows[i]
		if r.State == "removed" {
			continue
		}
		total++
		if r.State == "running" {
			running++
		}
		if r.Health == "unhealthy" {
			unhealthyN++
			unhealthy = append(unhealthy, fmt.Sprintf("%s @ %s (%s)", r.Name, r.Hostname, r.State))
		}
		if r.FreshnessVerdict() == "outdated" {
			outdated = append(outdated, fmt.Sprintf("%s @ %s — %s", r.Name, r.Hostname, r.Image))
		}
	}
	var badAgents []string
	for _, a := range agents {
		if a.LastStatus == "stale" || a.LastStatus == "offline" {
			badAgents = append(badAgents, fmt.Sprintf("%s (%s)", a.Name, a.LastStatus))
		}
	}

	sinceUnix := since.Unix()
	var recent []store.EventRow
	for _, e := range events {
		if e.At >= sinceUnix {
			recent = append(recent, e)
		}
	}

	subject = fmt.Sprintf("Trove digest: %d services, %d unhealthy, %d outdated", total, unhealthyN, len(outdated))

	var b strings.Builder
	fmt.Fprintf(&b, "Trove digest — %s\n\n", d.now().Format("Mon 2 Jan 2006 15:04"))
	fmt.Fprintf(&b, "Summary: %d services across %d agents · %d running · %d unhealthy · %d outdated\n\n",
		total, len(agents), running, unhealthyN, len(outdated))
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "  - %s\n", it)
		}
		b.WriteString("\n")
	}
	section("Agents not reporting", badAgents)
	section("Unhealthy services", unhealthy)
	section("Updates available", outdated)
	if len(recent) > 0 {
		fmt.Fprintf(&b, "Activity since %s (%d events):\n", since.Format("2 Jan 15:04"), len(recent))
		max := len(recent)
		if max > 30 {
			max = 30
		}
		for _, e := range recent[:max] {
			subjectName := e.Service
			if e.Kind == store.EventKindAgent {
				subjectName = "agent " + e.Agent
			}
			fmt.Fprintf(&b, "  - %s  %s %s → %s\n",
				time.Unix(e.At, 0).Format("2 Jan 15:04"), subjectName, orNone(e.FromState), e.ToState)
		}
		if len(recent) > max {
			fmt.Fprintf(&b, "  … and %d more\n", len(recent)-max)
		}
	} else {
		b.WriteString("No state changes since the last digest.\n")
	}
	text = b.String()

	// Minimal HTML alternative: the text body in a styled <pre>.
	htmlBody = fmt.Sprintf(
		`<html><body style="background:#1e1e2e;color:#cdd6f4;padding:16px">`+
			`<pre style="font-family:ui-monospace,Menlo,monospace;font-size:13px;line-height:1.5">%s</pre>`+
			`</body></html>`, html.EscapeString(text))
	return subject, text, htmlBody, nil
}
