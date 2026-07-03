// Package model defines the wire contract shared between Trove agents and the
// Trove server. Agents import this package; it must stay free of server- or
// store-specific concerns so that future agents (k8s, proxmox, bare metal) can
// depend on it without pulling in the world.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Health is the normalized health enum. Agents map platform-specific status
// into one of these values; the server derives Stale on its own from
// heartbeat timing (agents never report Stale themselves).
type Health string

const (
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
	HealthUnknown   Health = "unknown"
	HealthStale     Health = "stale"
)

// Valid reports whether h is a health value an agent is allowed to send.
// HealthStale is intentionally excluded: staleness is server-derived.
func (h Health) Valid() bool {
	switch h {
	case HealthHealthy, HealthUnhealthy, HealthUnknown:
		return true
	default:
		return false
	}
}

// Kind is the class of thing a service represents. Only Container is used in
// Phase 1; the rest are reserved so the schema and API don't need to change
// when later agents arrive.
type Kind string

const (
	KindContainer Kind = "container"
	KindPod       Kind = "pod"
	KindVM        Kind = "vm"
	KindLXC       Kind = "lxc"
	KindProcess   Kind = "process"
)

func (k Kind) Valid() bool {
	switch k {
	case KindContainer, KindPod, KindVM, KindLXC, KindProcess:
		return true
	default:
		return false
	}
}

// Platform identifies the agent type. Only Docker exists in Phase 1.
const (
	PlatformDocker = "docker"
)

// StateRemoved is the synthetic state the server assigns to a service that was
// previously reported but is absent from the latest full-state report. Agents
// never send this value.
const StateRemoved = "removed"

// Report is the full-state payload an agent POSTs to /api/v1/report. Reports
// are full snapshots, not deltas: the server replaces its view of the host's
// services with exactly what the report contains. This is idempotent and
// tolerates lost pushes.
type Report struct {
	Agent    ReportAgent     `json:"agent"`
	Host     ReportHost      `json:"host"`
	Services []ReportService `json:"services"`
}

// ReportAgent identifies the pushing agent.
type ReportAgent struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
	// IntervalSeconds is the agent's configured push interval. The server
	// stores it and derives staleness thresholds per-agent (stale/offline are
	// multiples of this), so a slow-polling agent isn't falsely flagged. Zero
	// or absent means "use the server default".
	IntervalSeconds int `json:"interval_seconds,omitempty"`
}

// ReportHost describes the machine the agent runs on. Meta carries
// platform-specific facts (e.g. docker_version) that are useful to surface but
// not worth first-class columns.
type ReportHost struct {
	Hostname string            `json:"hostname"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// ReportService is one catalog entry: a container now, a pod/vm/lxc later.
type ReportService struct {
	// ExternalID is the platform-native identifier (e.g. container ID). It is
	// stable across reports and unique within a host, and is what the server
	// uses to correlate a service across pushes.
	ExternalID  string            `json:"external_id"`
	Name        string            `json:"name"`
	Kind        Kind              `json:"kind"`
	Image       string            `json:"image"`
	ImageDigest string            `json:"image_digest,omitempty"`
	State       string            `json:"state"`
	Health      Health            `json:"health"`
	Ports       []Port            `json:"ports,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Port is a published port mapping.
type Port struct {
	Host      int    `json:"host"`
	Container int    `json:"container"`
	Proto     string `json:"proto"`
}

// Validate performs cheap structural checks on an inbound report so the
// ingest handler can reject malformed pushes with a 400 before touching the
// store. It does not enforce business rules beyond the wire contract.
func (r *Report) Validate() error {
	if strings.TrimSpace(r.Agent.Name) == "" {
		return errors.New("agent.name is required")
	}
	if strings.TrimSpace(r.Agent.Platform) == "" {
		return errors.New("agent.platform is required")
	}
	if strings.TrimSpace(r.Host.Hostname) == "" {
		return errors.New("host.hostname is required")
	}
	seen := make(map[string]struct{}, len(r.Services))
	for i, s := range r.Services {
		if strings.TrimSpace(s.ExternalID) == "" {
			return fmt.Errorf("services[%d].external_id is required", i)
		}
		if _, dup := seen[s.ExternalID]; dup {
			return fmt.Errorf("services[%d].external_id %q is duplicated within the report", i, s.ExternalID)
		}
		seen[s.ExternalID] = struct{}{}
		if !s.Kind.Valid() {
			return fmt.Errorf("services[%d].kind %q is not a recognized kind", i, s.Kind)
		}
		if !s.Health.Valid() {
			return fmt.Errorf("services[%d].health %q is not an agent-reportable health value", i, s.Health)
		}
	}
	return nil
}
