package alert

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/internal/store"
)

const cursorKey = "alert_cursor"

// Engine consumes the event stream and the freshness state, deciding what to
// notify and delivering through the configured dispatchers.
type Engine struct {
	store       *store.Store
	log         *slog.Logger
	cfg         Config
	dispatchers []Dispatcher
	now         func() time.Time
}

// NewEngine builds an engine; dispatchers derive from cfg.
func NewEngine(st *store.Store, log *slog.Logger, cfg Config) *Engine {
	return &Engine{
		store:       st,
		log:         log,
		cfg:         cfg,
		dispatchers: Dispatchers(cfg),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Run sweeps until ctx is cancelled. No-op (returns immediately) when no
// instant channel is configured.
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled() {
		e.log.Info("alerting disabled (no channel configured)")
		return
	}
	names := make([]string, 0, len(e.dispatchers))
	for _, d := range e.dispatchers {
		names = append(names, d.Name())
	}
	e.log.Info("alerting enabled", "channels", strings.Join(names, ","),
		"kinds", kindList(e.cfg.Kinds), "cooldown", e.cfg.Cooldown)

	t := time.NewTicker(e.cfg.Interval)
	defer t.Stop()
	e.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Sweep(ctx)
		}
	}
}

// Sweep runs one evaluation pass: consume new events, then check freshness
// transitions. Exported for tests and for a deterministic first pass.
func (e *Engine) Sweep(ctx context.Context) {
	e.sweepEvents(ctx)
	if e.cfg.Kinds["freshness"] {
		e.sweepFreshness(ctx)
	}
}

// ---- event stream ----------------------------------------------------------

func (e *Engine) sweepEvents(ctx context.Context) {
	raw, ok, err := e.store.GetMeta(ctx, cursorKey)
	if err != nil {
		e.log.Error("alert: read cursor", "err", err)
		return
	}
	if !ok {
		// First run: seed at the stream head so history is never replayed.
		maxID, err := e.store.MaxEventID(ctx)
		if err != nil {
			e.log.Error("alert: seed cursor", "err", err)
			return
		}
		if err := e.store.SetMeta(ctx, cursorKey, strconv.FormatInt(maxID, 10)); err != nil {
			e.log.Error("alert: store cursor", "err", err)
		}
		return
	}
	cursor, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		// A corrupted cursor must never silently become 0 — that would replay
		// the entire event history and re-fire every historical transition.
		// Re-seed at the stream head instead, same as a fresh install.
		e.log.Error("alert: corrupt cursor, reseeding at stream head", "raw", raw, "err", perr)
		maxID, merr := e.store.MaxEventID(ctx)
		if merr != nil {
			e.log.Error("alert: reseed cursor", "err", merr)
			return
		}
		if err := e.store.SetMeta(ctx, cursorKey, strconv.FormatInt(maxID, 10)); err != nil {
			e.log.Error("alert: store cursor", "err", err)
		}
		return
	}

	events, err := e.store.EventsAfter(ctx, cursor, 500)
	if err != nil {
		e.log.Error("alert: read events", "err", err)
		return
	}
	if len(events) == 0 {
		return
	}
	for _, ev := range events {
		if e.cfg.Kinds[ev.Kind] {
			if key, n, verdict, ok := classify(ev); ok {
				if !e.deliver(ctx, key, verdict, n) {
					// Leave this event unconsumed so the next sweep can retry the
					// notification instead of marking a failed send as delivered.
					break
				}
			}
		}
		cursor = ev.ID
	}
	if err := e.store.SetMeta(ctx, cursorKey, strconv.FormatInt(cursor, 10)); err != nil {
		e.log.Error("alert: advance cursor", "err", err)
	}
}

// verdict classifies a transition target for alert-state tracking: "" means
// good/recovered, non-empty is the bad value being alerted, and skip means no
// notification is warranted.
type verdictT struct {
	bad   string // "" = good
	level string
}

// classify turns an event into (state key, notification, verdict). ok=false
// means the event is feed-only (e.g. appearances, mass stale flips).
func classify(ev store.EventRow) (string, Notification, verdictT, bool) {
	n := Notification{
		Kind:    ev.Kind,
		Host:    ev.Hostname,
		Service: ev.Service,
		Agent:   ev.Agent,
		From:    ev.FromState,
		To:      ev.ToState,
		At:      time.Unix(ev.At, 0).UTC(),
	}
	where := ev.Hostname
	if where == "" {
		where = ev.Agent
	}

	switch ev.Kind {
	case store.EventKindAgent:
		key := "agent:" + strconv.FormatInt(ev.AgentID.Int64, 10)
		switch ev.ToState {
		case "offline":
			n.Level = LevelCritical
			n.Title = fmt.Sprintf("agent %s offline", ev.Agent)
			n.Body = fmt.Sprintf("agent %s stopped reporting (%s → offline)", ev.Agent, ev.FromState)
			return key, n, verdictT{bad: "offline", level: n.Level}, true
		case "stale":
			n.Level = LevelWarning
			n.Title = fmt.Sprintf("agent %s stale", ev.Agent)
			n.Body = fmt.Sprintf("agent %s has missed several reports (%s → stale)", ev.Agent, ev.FromState)
			return key, n, verdictT{bad: "stale", level: n.Level}, true
		case "ok":
			n.Level = LevelResolved
			n.Title = fmt.Sprintf("agent %s recovered", ev.Agent)
			n.Body = fmt.Sprintf("agent %s is reporting again (%s → ok)", ev.Agent, ev.FromState)
			return key, n, verdictT{bad: "", level: n.Level}, true
		}
		return "", n, verdictT{}, false

	case store.EventKindHealth:
		key := "svc:" + strconv.FormatInt(ev.ServiceID.Int64, 10) + ":health"
		switch ev.ToState {
		case "unhealthy":
			n.Level = LevelCritical
			n.Title = fmt.Sprintf("%s unhealthy", ev.Service)
			n.Body = fmt.Sprintf("%s @ %s: health %s → unhealthy", ev.Service, where, orNone(ev.FromState))
			return key, n, verdictT{bad: "unhealthy", level: n.Level}, true
		case "healthy":
			n.Level = LevelResolved
			n.Title = fmt.Sprintf("%s healthy again", ev.Service)
			n.Body = fmt.Sprintf("%s @ %s: health %s → healthy", ev.Service, where, orNone(ev.FromState))
			return key, n, verdictT{bad: "", level: n.Level}, true
		}
		// unknown/stale transitions (e.g. the mass flip when an agent goes
		// stale) are feed-only; the agent alert covers the root cause.
		return "", n, verdictT{}, false

	case store.EventKindState:
		if ev.FromState == "" {
			// Appearances are feed-only: alerting every new container/pod
			// would turn each deploy into a notification storm.
			return "", n, verdictT{}, false
		}
		key := "svc:" + strconv.FormatInt(ev.ServiceID.Int64, 10) + ":state"
		switch stateGoodness(ev.ToState) {
		case goodnessBad:
			n.Level = LevelWarning
			n.Title = fmt.Sprintf("%s %s", ev.Service, stateVerb(ev.ToState))
			n.Body = fmt.Sprintf("%s @ %s: %s → %s", ev.Service, where, ev.FromState, ev.ToState)
			return key, n, verdictT{bad: ev.ToState, level: n.Level}, true
		case goodnessGood:
			n.Level = LevelResolved
			n.Title = fmt.Sprintf("%s %s", ev.Service, stateVerb(ev.ToState))
			n.Body = fmt.Sprintf("%s @ %s: %s → %s", ev.Service, where, ev.FromState, ev.ToState)
			return key, n, verdictT{bad: "", level: n.Level}, true
		}
		return "", n, verdictT{}, false
	}
	return "", n, verdictT{}, false
}

type goodness int

const (
	goodnessNeutral goodness = iota
	goodnessGood
	goodnessBad
)

// stateGoodness classifies a platform state. K8s parents report
// "ready/desired"; everything else is a platform word.
func stateGoodness(state string) goodness {
	if ready, desired, ok := parseReplicas(state); ok {
		if desired > 0 && ready >= desired {
			return goodnessGood
		}
		return goodnessBad
	}
	switch state {
	case "running":
		return goodnessGood
	case "exited", "dead", "failed", "stopped", "removed":
		return goodnessBad
	default: // created, paused, restarting, pending, succeeded, ...
		return goodnessNeutral
	}
}

func stateVerb(state string) string {
	if ready, desired, ok := parseReplicas(state); ok {
		if desired > 0 && ready >= desired {
			return "fully ready (" + state + ")"
		}
		return "degraded (" + state + ")"
	}
	switch state {
	case "running":
		return "running again"
	case "exited", "dead", "stopped":
		return "stopped"
	case "failed":
		return "failed"
	case "removed":
		return "removed"
	default:
		return state
	}
}

func parseReplicas(s string) (ready, desired int, ok bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return 0, 0, false
	}
	r, err1 := strconv.Atoi(s[:i])
	d, err2 := strconv.Atoi(s[i+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return r, d, true
}

func orNone(s string) string {
	if s == "" {
		return "∅"
	}
	return s
}

// ---- freshness sweep -------------------------------------------------------

func (e *Engine) sweepFreshness(ctx context.Context) {
	rows, err := e.store.ListServices(ctx)
	if err != nil {
		e.log.Error("alert: freshness sweep", "err", err)
		return
	}
	for i := range rows {
		row := &rows[i]
		if row.State == "removed" || row.Image == "" || row.ImageDigest == "" {
			continue
		}
		verdict := row.FreshnessVerdict()
		// \x1f (unit separator) can't appear in agent/host/external_id values
		// an operator would type, unlike "/" — avoids two distinct services
		// colliding on the same key if a name happens to contain a slash.
		key := "fresh:" + row.AgentName + "\x1f" + row.Hostname + "\x1f" + row.ExternalID

		st, seen, err := e.store.GetAlertState(ctx, key)
		if err != nil {
			e.log.Error("alert: freshness state", "err", err)
			return
		}
		if !seen {
			// First sight seeds silently, unnotified — a fresh engine must not
			// announce fifteen already-outdated images at boot.
			seed := ""
			if verdict == "outdated" {
				seed = "outdated"
			}
			_ = e.store.SetAlertState(ctx, key, store.AlertState{Value: seed})
			continue
		}
		if st.Value == verdict {
			continue // no change
		}
		n := Notification{
			Kind:    "freshness",
			Host:    row.Hostname,
			Service: row.Name,
			From:    st.Value,
			To:      verdict,
			At:      e.now(),
		}
		switch {
		case verdict == "outdated":
			n.Level = LevelWarning
			n.Title = fmt.Sprintf("update available: %s", row.Name)
			n.Body = fmt.Sprintf("%s @ %s: %s has a newer image on its registry", row.Name, row.Hostname, row.Image)
			e.deliver(ctx, key, verdictT{bad: "outdated", level: n.Level}, n)
		case verdict == "current" && st.Value == "outdated":
			n.Level = LevelResolved
			n.Title = fmt.Sprintf("%s up to date", row.Name)
			n.Body = fmt.Sprintf("%s @ %s is now running the latest %s", row.Name, row.Hostname, row.Image)
			e.deliver(ctx, key, verdictT{bad: "", level: n.Level}, n)
		default:
			// verdict is "unknown" (a registry blip in either direction), or
			// "current" arriving straight from "" (never outdated). Neither
			// carries new information worth recording: leave the stored
			// value/notified bit untouched so a real "outdated" streak that's
			// bouncing through "unknown" still resolves correctly once the
			// registry answers again, instead of losing its history.
		}
	}
}

// ---- delivery --------------------------------------------------------------

// deliver applies recovery/cooldown semantics for key, then fans out.
//
// AlertState.Notified is tracked as its own field (see store.AlertState) so
// that recording an observed value never by itself erases the memory that an
// incident was actually announced — that memory must only change on an
// explicit send-or-not decision made here.
//
//   - good (verdict.bad == ""): send a resolved notice only if the current
//     bad streak was actually notified; always clear the stored value.
//   - bad, same value as already recorded AND already notified: this is the
//     same ongoing incident being re-observed (e.g. a health event replayed
//     after an unrelated agent reconnect) — do nothing, so nothing gets
//     reset or re-suppressed.
//   - bad, otherwise: within the cooldown window, record the new value as
//     not-yet-notified without sending (flap suppression), keeping the
//     original cooldown anchor. One exception: an escalation — a *worse*
//     value at critical level after an already-notified bad — bypasses
//     cooldown once (e.g. agent stale → offline).
func (e *Engine) deliver(ctx context.Context, key string, v verdictT, n Notification) bool {
	now := e.now().Unix()
	st, seen, err := e.store.GetAlertState(ctx, key)
	if err != nil {
		e.log.Error("alert: state read", "key", key, "err", err)
		return false
	}

	if v.bad == "" { // recovery
		if !seen || st.Value == "" {
			return true // nothing was bad; stay quiet
		}
		if st.Notified {
			if !e.send(ctx, deliveryKey(key, v), n) {
				return false
			}
			_ = e.store.SetAlertState(ctx, key, store.AlertState{Value: "", Notified: false, SentAt: now})
		} else {
			// The bad state was never announced — clear silently and keep the
			// cooldown anchor so an immediate re-flap doesn't reset the window.
			_ = e.store.SetAlertState(ctx, key, store.AlertState{Value: "", Notified: false, SentAt: st.SentAt})
		}
		return true
	}

	// bad path
	if seen && st.Value == v.bad && st.Notified {
		return true // same already-announced incident re-observed; nothing to do
	}
	inCooldown := seen && now-st.SentAt < int64(e.cfg.Cooldown.Seconds())
	escalation := st.Notified && st.Value != "" && st.Value != v.bad && v.level == LevelCritical
	if inCooldown && !escalation {
		if st.Value != v.bad || st.Notified {
			_ = e.store.SetAlertState(ctx, key, store.AlertState{Value: v.bad, Notified: false, SentAt: st.SentAt})
		}
		return true
	}
	if !e.send(ctx, deliveryKey(key, v), n) {
		return false
	}
	_ = e.store.SetAlertState(ctx, key, store.AlertState{Value: v.bad, Notified: true, SentAt: now})
	return true
}

// deliveryKey identifies one pending incident notification. It intentionally
// excludes the timestamp so retries of a freshness transition (which creates a
// new Notification timestamp on each sweep) still resume the same fan-out.
func deliveryKey(key string, v verdictT) string {
	if v.bad == "" {
		return key + "\x1frecovery"
	}
	return key + "\x1fbad\x1f" + v.bad
}

// send delivers n to every configured channel. Successful channels are
// persisted while any sibling channel is failing, so a retry resumes only the
// failed channels and does not duplicate notifications already accepted.
func (e *Engine) send(ctx context.Context, key string, n Notification) bool {
	allDelivered := true
	for _, d := range e.dispatchers {
		delivered, err := e.store.ChannelDelivered(ctx, key, d.Name())
		if err != nil {
			e.log.Error("alert: read channel delivery", "channel", d.Name(), "title", n.Title, "err", err)
			allDelivered = false
			continue
		}
		if delivered {
			continue
		}
		if err := d.Send(ctx, n); err != nil {
			e.log.Error("alert: send failed", "channel", d.Name(), "title", n.Title, "err", err)
			allDelivered = false
			continue
		}
		if err := e.store.MarkChannelDelivered(ctx, key, d.Name()); err != nil {
			e.log.Error("alert: record channel delivery", "channel", d.Name(), "title", n.Title, "err", err)
			allDelivered = false
			continue
		}
		e.log.Info("alert sent", "channel", d.Name(), "level", n.Level, "title", n.Title)
	}
	if !allDelivered {
		return false
	}
	if err := e.store.ClearChannelDeliveries(ctx, key); err != nil {
		e.log.Error("alert: clear channel deliveries", "title", n.Title, "err", err)
		return false
	}
	return true
}

// SendTest pushes a test notification through every configured channel and
// returns one error per failing channel (nil entries omitted).
func (e *Engine) SendTest(ctx context.Context) map[string]error {
	n := Notification{
		Kind:  "test",
		Level: LevelInfo,
		Title: "Trove test notification",
		Body:  "If you can read this, this channel is wired up correctly.",
		At:    e.now(),
	}
	results := map[string]error{}
	for _, d := range e.dispatchers {
		results[d.Name()] = d.Send(ctx, n)
	}
	return results
}

func kindList(m map[string]bool) string {
	var out []string
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	return strings.Join(out, ",")
}
