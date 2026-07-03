package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	Node string `json:"node"`
}

// pveResource is a guest entry from /cluster/resources?type=vm.
type pveResource struct {
	Type     string `json:"type"` // "qemu" or "lxc"
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Status   string `json:"status"` // running | stopped
	Node     string `json:"node"`
	Template int    `json:"template"` // 1 = template (not a real guest)
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
		byNode[r.Node] = append(byNode[r.Node], model.ReportService{
			ExternalID: fmt.Sprintf("%s/%d", r.Type, r.VMID),
			Name:       name,
			Kind:       kind,
			State:      r.Status,
			Health:     model.HealthUnknown, // Proxmox has no healthcheck; state carries up/down
			Labels:     map[string]string{"node": r.Node, "vmid": strconv.Itoa(r.VMID)},
		})
	}

	snaps := make([]agentkit.HostSnapshot, 0, len(nodesResp.Data))
	for _, n := range nodesResp.Data {
		snaps = append(snaps, agentkit.HostSnapshot{
			Host:     model.ReportHost{Hostname: n.Node, Meta: map[string]string{"platform": "proxmox"}},
			Services: byNode[n.Node],
		})
	}
	return snaps, nil
}
