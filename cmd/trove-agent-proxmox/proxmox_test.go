package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func TestLoadProxmoxConfigRequiresHTTPS(t *testing.T) {
	t.Setenv("TROVE_PROXMOX_URL", "http://pve.example:8006")
	t.Setenv("TROVE_PROXMOX_TOKEN", "root@pam!trove=test")
	t.Setenv("TROVE_PROXMOX_CA_FILE", "")
	t.Setenv("TROVE_PROXMOX_INSECURE", "")

	if _, err := loadProxmoxConfig(); err == nil {
		t.Fatal("loadProxmoxConfig accepted an HTTP URL")
	}
}

func TestProxmoxClientTrustsConfiguredCAFile(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "PVEAPIToken=root@pam!trove=test" {
			t.Fatalf("Authorization = %q", got)
		}
		writePVEResponse(t, w, []pveNode{{Node: "pve-a", Status: "online"}})
	}))
	defer api.Close()

	caPath := filepath.Join(t.TempDir(), "pve-root-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	t.Setenv("TROVE_PROXMOX_URL", api.URL)
	t.Setenv("TROVE_PROXMOX_TOKEN", "root@pam!trove=test")
	t.Setenv("TROVE_PROXMOX_CA_FILE", caPath)
	t.Setenv("TROVE_PROXMOX_INSECURE", "")

	cfg, err := loadProxmoxConfig()
	if err != nil {
		t.Fatalf("loadProxmoxConfig: %v", err)
	}
	var response struct {
		Data []pveNode `json:"data"`
	}
	if err := newProxmoxClient(cfg).get(context.Background(), "/api2/json/nodes", &response); err != nil {
		t.Fatalf("GET with configured CA: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Node != "pve-a" {
		t.Fatalf("response = %+v", response)
	}
}

func TestLoadProxmoxConfigRejectsCAWithInsecureMode(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "pve-root-ca.pem")
	if err := os.WriteFile(caPath, []byte("unused"), 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	t.Setenv("TROVE_PROXMOX_URL", "https://pve.example:8006")
	t.Setenv("TROVE_PROXMOX_TOKEN", "root@pam!trove=test")
	t.Setenv("TROVE_PROXMOX_CA_FILE", caPath)
	t.Setenv("TROVE_PROXMOX_INSECURE", "true")

	if _, err := loadProxmoxConfig(); err == nil {
		t.Fatal("loadProxmoxConfig accepted both CA_FILE and INSECURE")
	}
}

func TestCollectSetsGuestImageFromOSType(t *testing.T) {
	seen := map[string]bool{}
	cpuUsage := pveMetricFloat(0.125)
	uptime := uint64(90061)
	nodeStatus := pveNodeStatus{
		CPU:     &cpuUsage,
		LoadAvg: []pveMetricFloat{0.5, 0.25, 0.125},
		Memory:  pveNodeResourceUsage{Used: 8 * 1024 * 1024 * 1024, Total: 32 * 1024 * 1024 * 1024},
		RootFS:  pveNodeResourceUsage{Used: 40 * 1024 * 1024 * 1024, Total: 100 * 1024 * 1024 * 1024},
		Uptime:  &uptime,
	}
	nodeStatus.CPUInfo.CPUs = 16
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			writePVEResponse(t, w, []pveNode{{Node: "pve-a", Status: "online"}})
		case "/api2/json/nodes/pve-a/version":
			writePVEResponse(t, w, pveNodeVersion{Version: "8.4.1", Release: "1", RepoID: "a2b3c4d5"})
		case "/api2/json/nodes/pve-a/status":
			writePVEResponse(t, w, nodeStatus)
		case "/api2/json/cluster/resources":
			if got := r.URL.Query().Get("type"); got != "vm" {
				t.Fatalf("resources type query = %q, want vm", got)
			}
			writePVEResponse(t, w, []pveResource{
				{Type: "qemu", VMID: 101, Name: "winbox", Status: "running", Node: "pve-a", CPU: 0.03, MaxCPU: 4, Mem: 4 * 1024 * 1024 * 1024, MaxMem: 8 * 1024 * 1024 * 1024, Disk: 40 * 1024 * 1024 * 1024, MaxDisk: 100 * 1024 * 1024 * 1024, Uptime: 90061},
				{Type: "lxc", VMID: 202, Name: "debian-ct", Status: "running", Node: "pve-a", CPU: 0.01, MaxCPU: 2, Mem: 512 * 1024 * 1024, MaxMem: 1024 * 1024 * 1024, Disk: 2 * 1024 * 1024 * 1024, MaxDisk: 8 * 1024 * 1024 * 1024, Uptime: 3600},
			})
		case "/api2/json/nodes/pve-a/qemu/101/config":
			writePVEResponse(t, w, pveGuestConfig{OSType: "win11"})
		case "/api2/json/nodes/pve-a/lxc/202/config":
			writePVEResponse(t, w, pveGuestConfig{OSType: "debian"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	col := &collector{
		cli: newProxmoxClient(proxmoxConfig{url: api.URL, token: "root@pam!trove=test"}),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	snaps, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	if got := snaps[0].Host.Meta["platform"]; got != "proxmox" {
		t.Fatalf("host platform meta = %q, want proxmox", got)
	}
	if got := snaps[0].Host.Meta["proxmox.version"]; got != "8.4.1" {
		t.Fatalf("host proxmox.version meta = %q, want 8.4.1", got)
	}
	if got := snaps[0].Host.Meta["proxmox.release"]; got != "1" {
		t.Fatalf("host proxmox.release meta = %q, want 1", got)
	}
	if got := snaps[0].Host.Meta["proxmox.repoid"]; got != "a2b3c4d5" {
		t.Fatalf("host proxmox.repoid meta = %q, want a2b3c4d5", got)
	}
	if snaps[0].Host.Condition != model.HostConditionNormal {
		t.Fatalf("host condition = %q, want normal", snaps[0].Host.Condition)
	}
	metrics := snaps[0].Host.Metrics
	if metrics == nil || metrics.CPUUsageRatio == nil || *metrics.CPUUsageRatio != 0.125 ||
		metrics.CPULogicalCount == nil || *metrics.CPULogicalCount != 16 ||
		metrics.Load1 == nil || *metrics.Load1 != 0.5 ||
		metrics.Memory == nil || metrics.Memory.UsedBytes != 8*1024*1024*1024 ||
		metrics.RootDisk == nil || metrics.RootDisk.TotalBytes != 100*1024*1024*1024 ||
		metrics.UptimeSeconds == nil || *metrics.UptimeSeconds != 90061 {
		t.Fatalf("host metrics = %+v", metrics)
	}
	services := snaps[0].Services
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ExternalID < services[j].ExternalID })

	assertServiceImage(t, services[0], "lxc/202", model.KindLXC, "Debian", "debian")
	assertServiceImage(t, services[1], "qemu/101", model.KindVM, "Windows 11", "win11")
	if services[1].Health != model.HealthHealthy {
		t.Fatalf("qemu/101 health = %q, want healthy", services[1].Health)
	}
	if services[1].HealthDetail != "Running · 1d 1h · CPU 3% · RAM 50% · disk 40%" {
		t.Fatalf("qemu/101 health detail = %q", services[1].HealthDetail)
	}
	if got := services[1].Labels["proxmox.mem_pct"]; got != "50%" {
		t.Fatalf("qemu/101 mem pct label = %q, want 50%%", got)
	}
	if got := services[1].Labels["proxmox.uptime"]; got != "1d 1h" {
		t.Fatalf("qemu/101 uptime label = %q, want 1d 1h", got)
	}

	for _, path := range []string{
		"/api2/json/nodes/pve-a/version",
		"/api2/json/nodes/pve-a/status",
		"/api2/json/nodes/pve-a/qemu/101/config",
		"/api2/json/nodes/pve-a/lxc/202/config",
	} {
		if !seen[path] {
			t.Fatalf("expected config endpoint %s to be queried", path)
		}
	}
}

func TestCollectReportsOfflineNodeConditionWithoutMetricsRequest(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			writePVEResponse(t, w, []pveNode{{Node: "pve-offline", Status: "offline"}})
		case "/api2/json/cluster/resources":
			writePVEResponse(t, w, []pveResource{{
				Type: "qemu", VMID: 101, Name: "offline-guest", Status: "running", Node: "pve-offline",
			}})
		case "/api2/json/nodes/pve-offline/version":
			t.Fatal("offline node version endpoint must not be queried")
		case "/api2/json/nodes/pve-offline/status":
			t.Fatal("offline node status endpoint must not be queried")
		case "/api2/json/nodes/pve-offline/qemu/101/config":
			t.Fatal("offline guest config endpoint must not be queried")
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	col := &collector{
		cli: newProxmoxClient(proxmoxConfig{url: api.URL, token: "root@pam!trove=test"}),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	snaps, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Host.Condition != model.HostConditionCritical || snaps[0].Host.Metrics != nil {
		t.Fatalf("offline snapshot = %+v", snaps)
	}
	if len(snaps[0].Services) != 1 || snaps[0].Services[0].ExternalID != "qemu/101" ||
		snaps[0].Services[0].Image != "" || snaps[0].Services[0].Health != model.HealthHealthy {
		t.Fatalf("offline guest snapshot = %+v", snaps[0].Services)
	}
}

func TestCollectKeepsOnlineNodeWhenMetricsUnavailable(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			writePVEResponse(t, w, []pveNode{{Node: "pve-a", Status: "online"}})
		case "/api2/json/cluster/resources":
			writePVEResponse(t, w, []pveResource{})
		case "/api2/json/nodes/pve-a/version":
			writePVEResponse(t, w, pveNodeVersion{})
		case "/api2/json/nodes/pve-a/status":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	col := &collector{
		cli: newProxmoxClient(proxmoxConfig{url: api.URL, token: "root@pam!trove=test"}),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	snaps, err := col.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Host.Condition != model.HostConditionNormal || snaps[0].Host.Metrics != nil {
		t.Fatalf("online snapshot with missing metrics = %+v", snaps)
	}
}

func TestPVEMetricFloatAcceptsQuotedAndNumericValues(t *testing.T) {
	var values []pveMetricFloat
	if err := json.Unmarshal([]byte(`["0.25",0.5,"0"]`), &values); err != nil {
		t.Fatalf("unmarshal metric values: %v", err)
	}
	if len(values) != 3 || values[0] != 0.25 || values[1] != 0.5 || values[2] != 0 {
		t.Fatalf("metric values = %+v", values)
	}
}

func TestProxmoxGuestHealth(t *testing.T) {
	cases := []struct {
		name       string
		resource   pveResource
		wantHealth model.Health
		wantDetail string
	}{
		{
			name:       "stopped is neutral",
			resource:   pveResource{Status: "stopped"},
			wantHealth: model.HealthUnknown,
			wantDetail: "Guest is stopped",
		},
		{
			name: "running under normal load is healthy",
			resource: pveResource{
				Status: "running", CPU: 0.03, MaxCPU: 4,
				Mem: 4 * 1024 * 1024 * 1024, MaxMem: 8 * 1024 * 1024 * 1024,
				Disk: 40 * 1024 * 1024 * 1024, MaxDisk: 100 * 1024 * 1024 * 1024,
				Uptime: 90061,
			},
			wantHealth: model.HealthHealthy,
			wantDetail: "Running · 1d 1h · CPU 3% · RAM 50% · disk 40%",
		},
		{
			// Regression guard: resource pressure must NOT map to unhealthy.
			// High memory is normal for a running guest and would otherwise
			// fire spurious events/alerts.
			name:       "running near memory limit stays healthy",
			resource:   pveResource{Status: "running", Mem: 96, MaxMem: 100},
			wantHealth: model.HealthHealthy,
			wantDetail: "Running · CPU 0% · RAM 96%",
		},
		{
			name:       "running near disk limit stays healthy",
			resource:   pveResource{Status: "running", Disk: 97, MaxDisk: 100},
			wantHealth: model.HealthHealthy,
			wantDetail: "Running · CPU 0% · disk 97%",
		},
		{
			name:       "unexpected state is unknown",
			resource:   pveResource{Status: "suspended"},
			wantHealth: model.HealthUnknown,
			wantDetail: "Unexpected Proxmox status: suspended",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHealth, gotDetail := proxmoxGuestHealth(tc.resource)
			if gotHealth != tc.wantHealth || gotDetail != tc.wantDetail {
				t.Fatalf("proxmoxGuestHealth() = %q/%q, want %q/%q", gotHealth, gotDetail, tc.wantHealth, tc.wantDetail)
			}
		})
	}
}

func TestDisplayOSType(t *testing.T) {
	cases := map[string]string{
		"win11":     "Windows 11",
		"win10":     "Windows 10",
		"l26":       "Linux",
		"ubuntu":    "Ubuntu",
		"debian":    "Debian",
		"opensuse":  "openSUSE",
		"unmanaged": "Unmanaged",
		"customOS":  "customOS",
		"":          "",
	}
	for in, want := range cases {
		if got := displayOSType(in); got != want {
			t.Fatalf("displayOSType(%q) = %q, want %q", in, got, want)
		}
	}
}

func assertServiceImage(t *testing.T, svc model.ReportService, extID string, kind model.Kind, image, ostype string) {
	t.Helper()
	if svc.ExternalID != extID || svc.Kind != kind {
		t.Fatalf("service = %s/%s, want %s/%s", svc.Kind, svc.ExternalID, kind, extID)
	}
	if svc.Image != image {
		t.Fatalf("%s image = %q, want %q", extID, svc.Image, image)
	}
	if svc.ImageDigest != "" {
		t.Fatalf("%s image digest = %q, want empty for Proxmox OS image", extID, svc.ImageDigest)
	}
	if svc.Labels["ostype"] != ostype {
		t.Fatalf("%s ostype label = %q, want %q", extID, svc.Labels["ostype"], ostype)
	}
}

func writePVEResponse(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
