package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/techdox/trove/internal/agentkit"
	"github.com/techdox/trove/pkg/model"
)

// proxmoxConfig is the Proxmox-specific configuration.
type proxmoxConfig struct {
	url      string // e.g. https://pve.example:8006
	token    string // USER@REALM!TOKENID=SECRET
	insecure bool   // skip TLS verification (common for self-signed homelab certs)
}

func loadProxmoxConfig() (proxmoxConfig, error) {
	var c proxmoxConfig
	c.url = strings.TrimRight(os.Getenv("TROVE_PROXMOX_URL"), "/")
	if c.url == "" {
		return c, fmt.Errorf("TROVE_PROXMOX_URL is required (e.g. https://pve.example:8006)")
	}
	c.token = os.Getenv("TROVE_PROXMOX_TOKEN")
	if c.token == "" {
		return c, fmt.Errorf("TROVE_PROXMOX_TOKEN is required (format USER@REALM!TOKENID=SECRET)")
	}
	c.insecure, _ = strconv.ParseBool(os.Getenv("TROVE_PROXMOX_INSECURE"))
	return c, nil
}

// proxmoxClient is a read-only client for the Proxmox VE API. It only issues
// GETs against the API token's granted scope.
type proxmoxClient struct {
	http  *http.Client
	base  string
	token string
}

func newProxmoxClient(cfg proxmoxConfig) *proxmoxClient {
	tr := &http.Transport{}
	if cfg.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for homelab self-signed certs
	}
	return &proxmoxClient{
		http:  &http.Client{Timeout: 20 * time.Second, Transport: tr},
		base:  cfg.url,
		token: cfg.token,
	}
}

func (c *proxmoxClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("proxmox GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// pveNode is an entry from /nodes.
type pveNode struct {
	Node   string `json:"node"`
	Status string `json:"status"`
}

// pveNodeVersion is returned by /nodes/{node}/version.
type pveNodeVersion struct {
	Version string `json:"version"`
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
}

// pveMetricFloat accepts the Proxmox API's numeric metrics in either their
// number or quoted-number form. loadavg has used both representations across
// API versions, so being liberal here keeps collection compatible without
// weakening Trove's validated wire contract.
type pveMetricFloat float64

func (f *pveMetricFloat) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("parse Proxmox metric %q: %w", raw, err)
	}
	*f = pveMetricFloat(v)
	return nil
}

type pveNodeResourceUsage struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

// pveNodeStatus is the resource summary returned by /nodes/{node}/status.
type pveNodeStatus struct {
	CPU     *pveMetricFloat `json:"cpu"`
	CPUInfo struct {
		CPUs int `json:"cpus"`
	} `json:"cpuinfo"`
	LoadAvg []pveMetricFloat     `json:"loadavg"`
	Memory  pveNodeResourceUsage `json:"memory"`
	RootFS  pveNodeResourceUsage `json:"rootfs"`
	Uptime  *uint64              `json:"uptime"`
}

// pveResource is a guest entry from /cluster/resources?type=vm.
type pveResource struct {
	Type     string  `json:"type"` // "qemu" or "lxc"
	VMID     int     `json:"vmid"`
	Name     string  `json:"name"`
	Status   string  `json:"status"` // running | stopped
	Node     string  `json:"node"`
	Template int     `json:"template"` // 1 = template (not a real guest)
	CPU      float64 `json:"cpu"`      // ratio, e.g. 0.03 = 3%
	MaxCPU   int     `json:"maxcpu"`
	Mem      uint64  `json:"mem"`
	MaxMem   uint64  `json:"maxmem"`
	Disk     uint64  `json:"disk"`
	MaxDisk  uint64  `json:"maxdisk"`
	Uptime   uint64  `json:"uptime"` // seconds
}

// pveGuestConfig is the tiny subset of a QEMU/LXC config we need. `ostype` is
// enough to show a useful "Image" value for non-container Proxmox guests
// without relying on the QEMU guest agent.
type pveGuestConfig struct {
	OSType string `json:"ostype"`
}

// collector maps a Proxmox cluster to per-node Trove snapshots.
type collector struct {
	cli *proxmoxClient
	log *slog.Logger
}

// Collect returns one HostSnapshot per Proxmox node (including nodes with no
// guests, so full-state soft-removal works when the last guest leaves a node).
func (c *collector) Collect(ctx context.Context) ([]agentkit.HostSnapshot, error) {
	var nodesResp struct {
		Data []pveNode `json:"data"`
	}
	if err := c.cli.get(ctx, "/api2/json/nodes", &nodesResp); err != nil {
		return nil, err
	}
	conditionByNode := make(map[string]model.HostCondition, len(nodesResp.Data))
	for _, n := range nodesResp.Data {
		conditionByNode[n.Node] = proxmoxNodeCondition(n.Status)
	}
	var resResp struct {
		Data []pveResource `json:"data"`
	}
	if err := c.cli.get(ctx, "/api2/json/cluster/resources?type=vm", &resResp); err != nil {
		return nil, err
	}

	byNode := map[string][]model.ReportService{}
	for _, r := range resResp.Data {
		if r.Template == 1 {
			continue // templates aren't running services
		}
		var kind model.Kind
		switch r.Type {
		case "qemu":
			kind = model.KindVM
		case "lxc":
			kind = model.KindLXC
		default:
			continue
		}
		name := r.Name
		if name == "" {
			name = fmt.Sprintf("%s-%d", r.Type, r.VMID)
		}
		var image, osType string
		// Cluster resources can retain guests for an offline node. Keep those
		// guests in the catalogue, but do not issue node-local config requests
		// that are expected to fail or wait for the HTTP timeout while it is down.
		if conditionByNode[r.Node] != model.HostConditionCritical {
			image, osType = c.guestImage(ctx, r)
		}
		health, healthDetail := proxmoxGuestHealth(r)
		labels := proxmoxLabels(r)
		if osType != "" {
			labels["ostype"] = osType
		}
		byNode[r.Node] = append(byNode[r.Node], model.ReportService{
			ExternalID:   fmt.Sprintf("%s/%d", r.Type, r.VMID),
			Name:         name,
			Kind:         kind,
			Image:        image,
			State:        r.Status,
			Health:       health,
			HealthDetail: healthDetail,
			Labels:       labels,
		})
	}

	snaps := make([]agentkit.HostSnapshot, 0, len(nodesResp.Data))
	for _, n := range nodesResp.Data {
		condition := conditionByNode[n.Node]
		var metrics *model.HostMetrics
		meta := map[string]string{"platform": "proxmox"}
		// Offline cluster members cannot answer their node-local status endpoint.
		// Preserve the useful critical condition without logging expected 5xx
		// responses from either node-local endpoint every collection interval.
		if condition != model.HostConditionCritical {
			metrics = c.nodeMetrics(ctx, n.Node)
			meta = c.nodeMeta(ctx, n.Node)
		}
		snaps = append(snaps, agentkit.HostSnapshot{
			Host: model.ReportHost{
				Hostname:  n.Node,
				Condition: condition,
				Metrics:   metrics,
				Meta:      meta,
			},
			Services: byNode[n.Node],
		})
	}
	return snaps, nil
}

func proxmoxNodeCondition(status string) model.HostCondition {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online":
		return model.HostConditionNormal
	case "offline":
		return model.HostConditionCritical
	default:
		return model.HostConditionUnknown
	}
}

func (c *collector) nodeMetrics(ctx context.Context, node string) *model.HostMetrics {
	var resp struct {
		Data pveNodeStatus `json:"data"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/status", url.PathEscape(node))
	if err := c.cli.get(ctx, path, &resp); err != nil {
		c.log.Warn("proxmox: node metrics unavailable", "node", node, "err", err)
		return nil
	}

	m := &model.HostMetrics{}
	if resp.Data.CPU != nil {
		if cpu := float64(*resp.Data.CPU); isRatio(cpu) {
			m.CPUUsageRatio = &cpu
		}
	}
	if resp.Data.CPUInfo.CPUs > 0 {
		count := resp.Data.CPUInfo.CPUs
		m.CPULogicalCount = &count
	}
	for i := 0; i < len(resp.Data.LoadAvg) && i < 3; i++ {
		load := float64(resp.Data.LoadAvg[i])
		if !isNonNegativeFinite(load) {
			continue
		}
		switch i {
		case 0:
			m.Load1 = &load
		case 1:
			m.Load5 = &load
		case 2:
			m.Load15 = &load
		}
	}
	m.Memory = resourceUsage(resp.Data.Memory)
	m.RootDisk = resourceUsage(resp.Data.RootFS)
	if resp.Data.Uptime != nil {
		uptime := *resp.Data.Uptime
		m.UptimeSeconds = &uptime
	}
	return m
}

func isRatio(v float64) bool {
	return isNonNegativeFinite(v) && v <= 1
}

func isNonNegativeFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

func resourceUsage(v pveNodeResourceUsage) *model.HostResourceUsage {
	if v.Total == 0 || v.Used > v.Total {
		return nil
	}
	return &model.HostResourceUsage{UsedBytes: v.Used, TotalBytes: v.Total}
}

func (c *collector) nodeMeta(ctx context.Context, node string) map[string]string {
	meta := map[string]string{"platform": "proxmox"}

	var resp struct {
		Data pveNodeVersion `json:"data"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/version", url.PathEscape(node))
	if err := c.cli.get(ctx, path, &resp); err != nil {
		c.log.Warn("proxmox: node version unavailable", "node", node, "err", err)
		return meta
	}
	if v := strings.TrimSpace(resp.Data.Version); v != "" {
		meta["proxmox.version"] = v
	}
	if release := strings.TrimSpace(resp.Data.Release); release != "" {
		meta["proxmox.release"] = release
	}
	if repoID := strings.TrimSpace(resp.Data.RepoID); repoID != "" {
		meta["proxmox.repoid"] = repoID
	}
	return meta
}

// proxmoxGuestHealth maps a guest's power state to Trove's health enum.
//
// Health is deliberately NOT derived from resource usage. Proxmox reports
// infrastructure metrics, not an application healthcheck, and high memory is
// normal for a running guest — KVM guests trend toward ~100% of assigned RAM
// (caches, JVMs, databases, no ballooning), and flagging that as `unhealthy`
// would fire spurious events/alerts for ordinary VMs. The live CPU/memory/disk
// numbers are surfaced as informational metrics (see proxmoxLabels and the
// running detail line), not as a verdict.
func proxmoxGuestHealth(r pveResource) (model.Health, string) {
	status := strings.ToLower(strings.TrimSpace(r.Status))
	switch status {
	case "running":
		return model.HealthHealthy, runningDetail(r)
	case "stopped":
		return model.HealthUnknown, "Guest is stopped"
	case "":
		return model.HealthUnknown, "Proxmox did not report a guest status"
	default:
		return model.HealthUnknown, "Unexpected Proxmox status: " + r.Status
	}
}

func runningDetail(r pveResource) string {
	parts := []string{"Running"}
	if r.Uptime > 0 {
		parts = append(parts, formatDuration(r.Uptime))
	}
	parts = append(parts, fmt.Sprintf("CPU %.0f%%", r.CPU*100))
	if pct, ok := percent(r.Mem, r.MaxMem); ok {
		parts = append(parts, fmt.Sprintf("RAM %.0f%%", pct))
	}
	if pct, ok := percent(r.Disk, r.MaxDisk); ok {
		parts = append(parts, fmt.Sprintf("disk %.0f%%", pct))
	}
	return strings.Join(parts, " · ")
}

func proxmoxLabels(r pveResource) map[string]string {
	labels := map[string]string{"node": r.Node, "vmid": strconv.Itoa(r.VMID)}
	labels["proxmox.cpu_pct"] = fmt.Sprintf("%.0f%%", r.CPU*100)
	if r.MaxCPU > 0 {
		labels["proxmox.maxcpu"] = strconv.Itoa(r.MaxCPU)
	}
	if r.Mem > 0 {
		labels["proxmox.mem_used"] = formatBytes(r.Mem)
	}
	if r.MaxMem > 0 {
		labels["proxmox.mem_total"] = formatBytes(r.MaxMem)
		if pct, ok := percent(r.Mem, r.MaxMem); ok {
			labels["proxmox.mem_pct"] = fmt.Sprintf("%.0f%%", pct)
		}
	}
	if r.Disk > 0 {
		labels["proxmox.disk_used"] = formatBytes(r.Disk)
	}
	if r.MaxDisk > 0 {
		labels["proxmox.disk_total"] = formatBytes(r.MaxDisk)
		if pct, ok := percent(r.Disk, r.MaxDisk); ok {
			labels["proxmox.disk_pct"] = fmt.Sprintf("%.0f%%", pct)
		}
	}
	if r.Uptime > 0 {
		labels["proxmox.uptime"] = formatDuration(r.Uptime)
	}
	return labels
}

func percent(used, total uint64) (float64, bool) {
	if total == 0 {
		return 0, false
	}
	return float64(used) / float64(total) * 100, true
}

func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func formatDuration(seconds uint64) string {
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func (c *collector) guestImage(ctx context.Context, r pveResource) (image, osType string) {
	pathKind := ""
	switch r.Type {
	case "qemu":
		pathKind = "qemu"
	case "lxc":
		pathKind = "lxc"
	default:
		return "", ""
	}

	var resp struct {
		Data pveGuestConfig `json:"data"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/config", url.PathEscape(r.Node), pathKind, r.VMID)
	if err := c.cli.get(ctx, path, &resp); err != nil {
		c.log.Warn("proxmox: guest config unavailable", "node", r.Node, "type", r.Type, "vmid", r.VMID, "err", err)
		return "", ""
	}
	osType = strings.TrimSpace(resp.Data.OSType)
	return displayOSType(osType), osType
}

func displayOSType(osType string) string {
	switch strings.ToLower(strings.TrimSpace(osType)) {
	case "":
		return ""
	case "win11":
		return "Windows 11"
	case "win10":
		return "Windows 10"
	case "win8":
		return "Windows 8"
	case "win7":
		return "Windows 7"
	case "wvista":
		return "Windows Vista"
	case "w2k8":
		return "Windows Server 2008"
	case "w2k3":
		return "Windows Server 2003"
	case "w2k":
		return "Windows 2000"
	case "wxp":
		return "Windows XP"
	case "l26":
		return "Linux"
	case "l24":
		return "Linux 2.4"
	case "debian":
		return "Debian"
	case "ubuntu":
		return "Ubuntu"
	case "alpine":
		return "Alpine"
	case "archlinux":
		return "Arch Linux"
	case "centos":
		return "CentOS"
	case "devuan":
		return "Devuan"
	case "fedora":
		return "Fedora"
	case "gentoo":
		return "Gentoo"
	case "nixos":
		return "NixOS"
	case "opensuse":
		return "openSUSE"
	case "solaris":
		return "Solaris"
	case "other":
		return "Other"
	case "unmanaged":
		return "Unmanaged"
	default:
		return osType
	}
}
