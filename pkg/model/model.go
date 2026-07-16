// Package model defines the wire contract shared between Trove agents and the
// Trove server. Agents import this package; it must stay free of server- or
// store-specific concerns so that future agents (k8s, proxmox, bare metal) can
// depend on it without pulling in the world.
package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// Kubernetes parent workloads (children are pods, linked via parent).
	KindDeployment  Kind = "deployment"
	KindStatefulSet Kind = "statefulset"
	KindDaemonSet   Kind = "daemonset"
)

func (k Kind) Valid() bool {
	switch k {
	case KindContainer, KindPod, KindVM, KindLXC, KindProcess,
		KindDeployment, KindStatefulSet, KindDaemonSet:
		return true
	default:
		return false
	}
}

// Platform identifies the agent type. Every agent must report one of these
// values; the server validates it on ingest. The typed enum prevents
// silent typos at the call site (agents use the constants, turning a
// misspelling into a compile error) and lets the server reject unknown
// platforms with a 400 rather than storing a free-string.
type Platform string

const (
	PlatformDocker     Platform = "docker"
	PlatformKubernetes Platform = "kubernetes"
	PlatformProxmox    Platform = "proxmox"
	PlatformLocal      Platform = "local"
)

// Valid reports whether p is a recognized platform value.
func (p Platform) Valid() bool {
	switch p {
	case PlatformDocker, PlatformKubernetes, PlatformProxmox, PlatformLocal:
		return true
	default:
		return false
	}
}

// StateRemoved is the synthetic state the server assigns to a service that was
// previously reported but is absent from the latest full-state report. Agents
// never send this value.
const StateRemoved = "removed"

const (
	maxIdentityLength     = 255
	maxExternalIDLength   = 512
	maxStateLength        = 128
	maxImageLength        = 2048
	maxHealthDetailLength = 4096
)

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
	Name     string   `json:"name"`
	Platform Platform `json:"platform"`
	Version  string   `json:"version"`
	// IntervalSeconds is the agent's configured push interval. The server
	// stores it and derives staleness thresholds per-agent (stale/offline are
	// multiples of this), so a slow-polling agent isn't falsely flagged. Zero
	// or absent means "use the server default".
	IntervalSeconds int `json:"interval_seconds,omitempty"`
}

// HostCondition is the platform-reported condition of a host. It is separate
// from the server-derived heartbeat status: a host can be reporting on time
// while its platform says it is degraded, or stop reporting while its last
// known platform condition was normal.
type HostCondition string

const (
	HostConditionNormal   HostCondition = "normal"
	HostConditionWarning  HostCondition = "warning"
	HostConditionCritical HostCondition = "critical"
	HostConditionUnknown  HostCondition = "unknown"
)

// Valid reports whether c is a recognized platform condition.
func (c HostCondition) Valid() bool {
	switch c {
	case HostConditionNormal, HostConditionWarning, HostConditionCritical, HostConditionUnknown:
		return true
	default:
		return false
	}
}

// HostResourceUsage is a current used/total capacity sample. It deliberately
// carries bytes rather than a percentage so API clients can choose their own
// display precision without losing the original values.
type HostResourceUsage struct {
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// HostMetrics is a current point-in-time resource snapshot. Pointer scalars
// preserve the difference between a real zero (for example an idle CPU) and a
// metric the platform did not provide. Trove stores only the latest snapshot;
// it is not intended to replace a time-series monitoring system.
type HostMetrics struct {
	CPUUsageRatio   *float64           `json:"cpu_usage_ratio,omitempty"`
	CPULogicalCount *int               `json:"cpu_logical_count,omitempty"`
	Load1           *float64           `json:"load_1,omitempty"`
	Load5           *float64           `json:"load_5,omitempty"`
	Load15          *float64           `json:"load_15,omitempty"`
	Memory          *HostResourceUsage `json:"memory,omitempty"`
	RootDisk        *HostResourceUsage `json:"root_disk,omitempty"`
	UptimeSeconds   *uint64            `json:"uptime_seconds,omitempty"`
}

// ReportHost describes the machine the agent runs on. Meta carries
// platform-specific facts (e.g. docker.version) that are useful to surface but
// not worth first-class columns. Condition and Metrics are typed because they
// have shared meaning across platforms and are rendered consistently.
type ReportHost struct {
	Hostname  string            `json:"hostname"`
	Condition HostCondition     `json:"condition,omitempty"`
	Metrics   *HostMetrics      `json:"metrics,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// ReportService is one catalog entry: a container now, a pod/vm/lxc later.
type ReportService struct {
	// ExternalID is the platform-native identifier (e.g. container ID). It is
	// stable across reports and unique within a host, and is what the server
	// uses to correlate a service across pushes.
	ExternalID string `json:"external_id"`
	// ParentExternalID, if set, is the ExternalID of this service's parent
	// within the same report/host — e.g. a pod's owning Deployment. The server
	// resolves it to an internal parent link. Empty for standalone services.
	ParentExternalID string `json:"parent_external_id,omitempty"`
	Name             string `json:"name"`
	Kind             Kind   `json:"kind"`
	Image            string `json:"image"`
	ImageDigest      string `json:"image_digest,omitempty"`
	State            string `json:"state"`
	Health           Health `json:"health"`
	// HealthDetail is an optional short, human-readable note about the current
	// state — e.g. a failing Docker healthcheck's last output, a Kubernetes
	// pod's waiting/termination reason, or a Proxmox guest's resource summary.
	// Empty when there's nothing to add.
	HealthDetail string            `json:"health_detail,omitempty"`
	Ports        []Port            `json:"ports,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
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
	if err := validateTextField("agent.name", r.Agent.Name, maxIdentityLength, true); err != nil {
		return err
	}
	if strings.TrimSpace(string(r.Agent.Platform)) == "" {
		return errors.New("agent.platform is required")
	}
	if !r.Agent.Platform.Valid() {
		return fmt.Errorf("agent.platform %q is not a recognized platform", r.Agent.Platform)
	}
	if err := validateTextField("agent.version", r.Agent.Version, maxIdentityLength, false); err != nil {
		return err
	}
	if err := validateTextField("host.hostname", r.Host.Hostname, maxIdentityLength, true); err != nil {
		return err
	}
	if r.Host.Condition != "" && !r.Host.Condition.Valid() {
		return fmt.Errorf("host.condition %q is not a recognized condition", r.Host.Condition)
	}
	if err := validateHostMetrics(r.Host.Metrics); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.Services))
	for i := range r.Services {
		s := &r.Services[i]
		if err := validateTextField(fmt.Sprintf("services[%d].external_id", i), s.ExternalID, maxExternalIDLength, true); err != nil {
			return err
		}
		if err := validateTextField(fmt.Sprintf("services[%d].parent_external_id", i), s.ParentExternalID, maxExternalIDLength, false); err != nil {
			return err
		}
		if err := validateTextField(fmt.Sprintf("services[%d].name", i), s.Name, maxIdentityLength, false); err != nil {
			return err
		}
		if _, dup := seen[s.ExternalID]; dup {
			return fmt.Errorf("services[%d].external_id %q is duplicated within the report", i, s.ExternalID)
		}
		seen[s.ExternalID] = struct{}{}
		if !s.Kind.Valid() {
			return fmt.Errorf("services[%d].kind %q is not a recognized kind", i, s.Kind)
		}
		if err := validateTextField(fmt.Sprintf("services[%d].state", i), s.State, maxStateLength, true); err != nil {
			return err
		}
		if s.State == StateRemoved {
			return fmt.Errorf("services[%d].state %q is server-derived", i, s.State)
		}
		if !s.Health.Valid() {
			return fmt.Errorf("services[%d].health %q is not an agent-reportable health value", i, s.Health)
		}
		if err := validateTextField(fmt.Sprintf("services[%d].image", i), s.Image, maxImageLength, false); err != nil {
			return err
		}
		if utf8.RuneCountInString(s.HealthDetail) > maxHealthDetailLength {
			return fmt.Errorf("services[%d].health_detail exceeds %d characters", i, maxHealthDetailLength)
		}
	}
	return nil
}

// validateTextField constrains attacker-influenced identifiers before they
// enter storage, alert titles, HTTP headers, or channel payloads. Human-facing
// identity fields never need control characters; rejecting them here avoids
// channel-specific header injection and poison-message failures.
func validateTextField(field, value string, maxLength int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxLength {
		return fmt.Errorf("%s exceeds %d characters", field, maxLength)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}

func validateHostMetrics(m *HostMetrics) error {
	if m == nil {
		return nil
	}
	if m.CPUUsageRatio != nil {
		v := *m.CPUUsageRatio
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return errors.New("host.metrics.cpu_usage_ratio must be a finite value between 0 and 1")
		}
	}
	if m.CPULogicalCount != nil && *m.CPULogicalCount < 1 {
		return errors.New("host.metrics.cpu_logical_count must be positive")
	}
	if err := validateNonNegativeMetric("load_1", m.Load1); err != nil {
		return err
	}
	if err := validateNonNegativeMetric("load_5", m.Load5); err != nil {
		return err
	}
	if err := validateNonNegativeMetric("load_15", m.Load15); err != nil {
		return err
	}
	if err := validateResourceUsage("memory", m.Memory); err != nil {
		return err
	}
	return validateResourceUsage("root_disk", m.RootDisk)
}

func validateNonNegativeMetric(name string, value *float64) error {
	if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
		return fmt.Errorf("host.metrics.%s must be a finite non-negative value", name)
	}
	return nil
}

func validateResourceUsage(name string, usage *HostResourceUsage) error {
	if usage == nil {
		return nil
	}
	if usage.TotalBytes == 0 {
		return fmt.Errorf("host.metrics.%s.total_bytes must be positive", name)
	}
	if usage.UsedBytes > usage.TotalBytes {
		return fmt.Errorf("host.metrics.%s.used_bytes must not exceed total_bytes", name)
	}
	return nil
}
