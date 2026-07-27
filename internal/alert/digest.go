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
//
// An invalid TROVE_DIGEST is logged as a warning (via log, which may be nil in
// tests) rather than silently disabling the digest with no diagnostic trail —
// the only other visible symptom would be the generic "digest disabled" line.
func LoadDigestConfigFromEnv(log *slog.Logger) DigestConfig {
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
	var err error
	cfg.Schedule, err = ParseSchedule(sched)
	if err != nil && log != nil {
		log.Warn("invalid TROVE_DIGEST, digest disabled", "value", sched, "err", err)
	}
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

type digestService struct {
	Name  string
	Host  string
	State string
	Image string
}

type digestActivity struct {
	At      time.Time
	Subject string
	From    string
	To      string
}

type digestView struct {
	GeneratedAt time.Time
	Since       time.Time
	Services    int
	Agents      int
	Running     int
	Unhealthy   int
	Outdated    int
	BadAgents   []string
	BadServices []digestService
	Updates     []digestService
	Activity    []digestActivity
	MoreEvents  int
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
	lastSent, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		// A corrupted timestamp must never silently become epoch zero — that
		// would summarize the entire event history as "since 1970". Re-anchor
		// to now instead, same as first boot.
		d.log.Error("digest: corrupt last-sent, re-anchoring to now", "raw", raw, "err", perr)
		_ = d.store.SetMeta(ctx, digestLastSentKey, strconv.FormatInt(now.Unix(), 10))
		return
	}
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
	var unhealthy, outdated []digestService
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
			unhealthy = append(unhealthy, digestService{Name: r.Name, Host: r.Hostname, State: r.State})
		}
		if r.FreshnessVerdict() == "outdated" {
			outdated = append(outdated, digestService{Name: r.Name, Host: r.Hostname, Image: r.Image})
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
	if len(unhealthy) > 0 {
		b.WriteString("Unhealthy services:\n")
		for _, svc := range unhealthy {
			fmt.Fprintf(&b, "  - %s @ %s (%s)\n", svc.Name, svc.Host, svc.State)
		}
		b.WriteString("\n")
	}
	if len(outdated) > 0 {
		b.WriteString("Updates available:\n")
		for _, svc := range outdated {
			fmt.Fprintf(&b, "  - %s @ %s — %s\n", svc.Name, svc.Host, svc.Image)
		}
		b.WriteString("\n")
	}
	var activity []digestActivity
	moreEvents := 0
	if len(recent) > 0 {
		fmt.Fprintf(&b, "Activity since %s (%d events):\n", since.Format("2 Jan 15:04"), len(recent))
		max := len(recent)
		if max > 30 {
			max = 30
		}
		for _, e := range recent[:max] {
			subjectName := e.Service
			switch e.Kind {
			case store.EventKindAgent:
				subjectName = "agent " + e.Agent
			case store.EventKindHost:
				subjectName = "host " + e.Hostname
			}
			activity = append(activity, digestActivity{
				At:      time.Unix(e.At, 0),
				Subject: subjectName,
				From:    orNone(e.FromState),
				To:      e.ToState,
			})
			fmt.Fprintf(&b, "  - %s  %s %s → %s\n",
				time.Unix(e.At, 0).Format("2 Jan 15:04"), subjectName, orNone(e.FromState), e.ToState)
		}
		if len(recent) > max {
			moreEvents = len(recent) - max
			fmt.Fprintf(&b, "  … and %d more\n", moreEvents)
		}
	} else {
		b.WriteString("No state changes since the last digest.\n")
	}
	text = b.String()

	htmlBody = renderDigestHTML(digestView{
		GeneratedAt: d.now(),
		Since:       since,
		Services:    total,
		Agents:      len(agents),
		Running:     running,
		Unhealthy:   unhealthyN,
		Outdated:    len(outdated),
		BadAgents:   badAgents,
		BadServices: unhealthy,
		Updates:     outdated,
		Activity:    activity,
		MoreEvents:  moreEvents,
	})
	return subject, text, htmlBody, nil
}

// renderDigestHTML builds a self-contained, table-based email. Inline styles
// keep the layout readable in clients with limited CSS support (notably
// Outlook), while the small media query stacks summary cards on narrow screens.
func renderDigestHTML(v digestView) string {
	var b strings.Builder
	esc := html.EscapeString
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>Trove fleet digest</title>
<style>
@media only screen and (max-width:620px) {
  .shell { width:100%% !important; }
  .pad { padding-left:20px !important; padding-right:20px !important; }
  .stat { display:inline-block !important; width:50%% !important; box-sizing:border-box !important; }
  .hide-mobile { display:none !important; }
}
</style>
</head>
<body style="margin:0;padding:0;background:#f3f5f9;color:#182230;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">%d services across %d agents · %d unhealthy · %d updates available</div>
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f5f9;">
<tr><td align="center" style="padding:28px 12px;">
<table role="presentation" class="shell" width="680" cellspacing="0" cellpadding="0" border="0" style="width:680px;max-width:680px;background:#ffffff;border-radius:16px;overflow:hidden;box-shadow:0 8px 28px rgba(16,24,40,.08);">
<tr><td class="pad" style="padding:34px 36px;background:#111827;color:#ffffff;">
  <div style="font-size:12px;line-height:18px;font-weight:700;letter-spacing:1.6px;color:#a7f3d0;">TROVE&nbsp;&nbsp;/&nbsp;&nbsp;FLEET DIGEST</div>
  <div style="margin-top:17px;font-size:28px;line-height:35px;font-weight:750;">%s</div>
  <div style="margin-top:8px;font-size:14px;line-height:21px;color:#cbd5e1;">%s · Activity since %s</div>
</td></tr>`,
		v.Services, v.Agents, v.Unhealthy, v.Outdated,
		digestHeadline(v), v.GeneratedAt.Format("Monday, 2 January 2006 at 15:04"), v.Since.Format("2 Jan at 15:04"))

	b.WriteString(`<tr><td class="pad" style="padding:28px 36px 8px;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0"><tr>`)
	statCard(&b, "Services", v.Services, "#2563eb", true)
	statCard(&b, "Running", v.Running, "#059669", true)
	statCard(&b, "Unhealthy", v.Unhealthy, "#dc2626", v.Unhealthy > 0)
	statCard(&b, "Updates", v.Outdated, "#d97706", v.Outdated > 0)
	fmt.Fprintf(&b, `</tr></table>
<div style="padding:11px 0 20px;font-size:13px;line-height:20px;color:#667085;">Reporting from <strong style="color:#344054;">%d agents</strong></div>
</td></tr>`, v.Agents)

	if len(v.BadAgents) == 0 && len(v.BadServices) == 0 {
		b.WriteString(`<tr><td class="pad" style="padding:4px 36px 20px;">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#ecfdf3;border:1px solid #abefc6;border-radius:10px;">
<tr><td style="padding:14px 16px;font-size:14px;line-height:20px;color:#067647;"><strong>✓ Core fleet health looks good</strong><br><span style="color:#357a5b;">All agents are reporting and no services are unhealthy.</span></td></tr>
</table></td></tr>`)
	}

	if len(v.BadAgents) > 0 {
		sectionTitle(&b, "Agents not reporting", len(v.BadAgents), "#b42318", "#fef3f2")
		b.WriteString(`<tr><td class="pad" style="padding:0 36px 24px;"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border:1px solid #fecdca;border-radius:10px;">`)
		for i, agent := range v.BadAgents {
			border := ""
			if i > 0 {
				border = "border-top:1px solid #fee4e2;"
			}
			fmt.Fprintf(&b, `<tr><td style="padding:13px 15px;%sfont-size:14px;color:#344054;">%s</td></tr>`, border, esc(agent))
		}
		b.WriteString(`</table></td></tr>`)
	}

	if len(v.BadServices) > 0 {
		sectionTitle(&b, "Unhealthy services", len(v.BadServices), "#b42318", "#fef3f2")
		b.WriteString(`<tr><td class="pad" style="padding:0 36px 24px;"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border:1px solid #fecdca;border-radius:10px;">`)
		for i, svc := range v.BadServices {
			serviceRow(&b, svc, i > 0, "#b42318", "#fef3f2", svc.State)
		}
		b.WriteString(`</table></td></tr>`)
	}

	if len(v.Updates) > 0 {
		sectionTitle(&b, "Updates available", len(v.Updates), "#b54708", "#fffaeb")
		b.WriteString(`<tr><td class="pad" style="padding:0 36px 24px;"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border:1px solid #e4e7ec;border-radius:10px;">`)
		for i, svc := range v.Updates {
			serviceRow(&b, svc, i > 0, "#b54708", "#fffaeb", "update")
		}
		b.WriteString(`</table></td></tr>`)
	}

	sectionTitle(&b, "Recent activity", len(v.Activity)+v.MoreEvents, "#344054", "#f2f4f7")
	b.WriteString(`<tr><td class="pad" style="padding:0 36px 32px;">`)
	if len(v.Activity) == 0 {
		b.WriteString(`<div style="padding:18px;border:1px solid #e4e7ec;border-radius:10px;font-size:14px;color:#667085;text-align:center;">No state changes since the last digest.</div>`)
	} else {
		b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="border:1px solid #e4e7ec;border-radius:10px;">`)
		for i, event := range v.Activity {
			border := ""
			if i > 0 {
				border = "border-top:1px solid #eaecf0;"
			}
			fmt.Fprintf(&b, `<tr>
<td class="hide-mobile" width="88" style="padding:12px 8px 12px 14px;%sfont-size:12px;line-height:18px;color:#667085;white-space:nowrap;">%s</td>
<td style="padding:12px 8px;%sfont-size:14px;line-height:20px;color:#344054;word-break:break-word;"><strong>%s</strong></td>
<td align="right" style="padding:12px 14px 12px 8px;%sfont-size:12px;line-height:18px;white-space:nowrap;"><span style="color:#667085;">%s</span>&nbsp; <span style="color:#98a2b3;">→</span>&nbsp; <strong style="color:%s;">%s</strong></td>
</tr>`, border, event.At.Format("2 Jan 15:04"), border, esc(event.Subject), border,
				esc(event.From), stateColor(event.To), esc(event.To))
		}
		b.WriteString(`</table>`)
		if v.MoreEvents > 0 {
			fmt.Fprintf(&b, `<div style="padding-top:12px;text-align:center;font-size:13px;color:#667085;">…and %d more events</div>`, v.MoreEvents)
		}
	}

	b.WriteString(`</td></tr>
<tr><td class="pad" style="padding:20px 36px;background:#f9fafb;border-top:1px solid #eaecf0;font-size:12px;line-height:18px;color:#667085;">
Automatically generated by <strong style="color:#344054;">Trove</strong> · Read-only fleet visibility
</td></tr>
</table>
</td></tr></table>
</body></html>`)
	return b.String()
}

func digestHeadline(v digestView) string {
	problems := len(v.BadAgents) + v.Unhealthy
	switch {
	case problems == 0 && v.Outdated == 0:
		return "Everything looks current"
	case problems == 0:
		return fmt.Sprintf("%d updates ready to review", v.Outdated)
	case problems == 1:
		return "1 item needs your attention"
	default:
		return fmt.Sprintf("%d items need your attention", problems)
	}
}

func statCard(b *strings.Builder, label string, value int, color string, emphasize bool) {
	background := "#f8fafc"
	if emphasize {
		background = color + "0d"
	}
	fmt.Fprintf(b, `<td class="stat" width="25%%" valign="top" style="padding:0 5px 0 0;">
<div style="padding:14px 12px;background:%s;border:1px solid #e4e7ec;border-radius:10px;">
<div style="font-size:12px;line-height:18px;color:#667085;">%s</div>
<div style="margin-top:2px;font-size:23px;line-height:29px;font-weight:750;color:%s;">%d</div>
</div></td>`, background, label, color, value)
}

func sectionTitle(b *strings.Builder, title string, count int, color, background string) {
	fmt.Fprintf(b, `<tr><td class="pad" style="padding:4px 36px 10px;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0"><tr>
<td style="font-size:16px;line-height:24px;font-weight:700;color:#182230;">%s</td>
<td align="right"><span style="display:inline-block;padding:2px 9px;border-radius:999px;background:%s;color:%s;font-size:12px;line-height:18px;font-weight:700;">%d</span></td>
</tr></table></td></tr>`, html.EscapeString(title), background, color, count)
}

func serviceRow(b *strings.Builder, svc digestService, bordered bool, color, background, badge string) {
	border := ""
	if bordered {
		border = "border-top:1px solid #eaecf0;"
	}
	image := ""
	if svc.Image != "" {
		image = fmt.Sprintf(`<div style="margin-top:3px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11px;line-height:17px;color:#667085;word-break:break-all;">%s</div>`, html.EscapeString(svc.Image))
	}
	fmt.Fprintf(b, `<tr>
<td style="padding:13px 14px;%s">
  <div style="font-size:14px;line-height:20px;font-weight:650;color:#344054;word-break:break-word;">%s</div>
  <div style="font-size:12px;line-height:18px;color:#667085;">%s</div>%s
</td>
<td align="right" valign="top" style="padding:13px 14px;%swhite-space:nowrap;"><span style="display:inline-block;padding:2px 8px;border-radius:999px;background:%s;color:%s;font-size:11px;line-height:17px;font-weight:700;">%s</span></td>
</tr>`, border, html.EscapeString(svc.Name), html.EscapeString(svc.Host), image,
		border, background, color, html.EscapeString(badge))
}

func stateColor(state string) string {
	switch strings.ToLower(state) {
	case "running", "healthy", "online", "current", "succeeded":
		return "#067647"
	case "removed", "stopped", "exited", "offline", "unhealthy", "failed":
		return "#b42318"
	case "created", "starting", "prelaunch":
		return "#b54708"
	default:
		return "#344054"
	}
}
