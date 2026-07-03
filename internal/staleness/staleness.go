// Package staleness holds the pure heartbeat-evaluation logic: given when an
// agent was last seen and its push interval, decide whether it is ok, stale,
// or offline. It has no I/O so it is trivially testable; the server runs a
// ticker that applies these verdicts to the store.
package staleness

import (
	"time"

	"trove/pkg/model"
)

// Status is an agent's heartbeat verdict.
type Status string

const (
	// StatusUnknown means the agent has never reported.
	StatusUnknown Status = "unknown"
	// StatusOK means the agent reported recently enough.
	StatusOK Status = "ok"
	// StatusStale means the agent has missed StaleAfterIntervals pushes.
	StatusStale Status = "stale"
	// StatusOffline means the agent has missed OfflineAfterIntervals pushes.
	StatusOffline Status = "offline"
)

// Interval returns the effective push interval for an agent: its configured
// value, or the server default when unset (0).
func Interval(agentIntervalSeconds int) time.Duration {
	if agentIntervalSeconds > 0 {
		return time.Duration(agentIntervalSeconds) * time.Second
	}
	return model.DefaultReportInterval()
}

// Evaluate returns the heartbeat status for an agent. lastSeen is nil if the
// agent has never reported. Thresholds are multiples of the agent's own
// interval, so a slow-polling agent is judged on its own cadence.
func Evaluate(lastSeen *time.Time, agentIntervalSeconds int, now time.Time) Status {
	if lastSeen == nil {
		return StatusUnknown
	}
	silence := now.Sub(*lastSeen)
	iv := Interval(agentIntervalSeconds)
	switch {
	case silence > time.Duration(model.OfflineAfterIntervals)*iv:
		return StatusOffline
	case silence > time.Duration(model.StaleAfterIntervals)*iv:
		return StatusStale
	default:
		return StatusOK
	}
}

// StaleOrWorse reports whether a status means the agent's services should be
// shown as stale (i.e. stale or offline).
func StaleOrWorse(s Status) bool {
	return s == StatusStale || s == StatusOffline
}
