package model

import "time"

// Heartbeat / staleness defaults, shared so the agent and server agree on the
// meaning of the numbers.
const (
	// DefaultReportIntervalSeconds is how often an agent pushes when not
	// overridden via TROVE_INTERVAL.
	DefaultReportIntervalSeconds = 30

	// StaleAfterIntervals: an agent that misses this many push intervals is
	// considered stale (its services are shown as stale). 3 * 30s = 90s.
	StaleAfterIntervals = 3

	// OfflineAfterIntervals: an agent silent for this many intervals is
	// considered offline. 10 * 30s = 300s (5 min).
	OfflineAfterIntervals = 10
)

// DefaultReportInterval is the typed convenience form of the default interval.
func DefaultReportInterval() time.Duration {
	return time.Duration(DefaultReportIntervalSeconds) * time.Second
}
