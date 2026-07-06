package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/techdox/trove/pkg/model"
)

func TestCollectSetsGuestImageFromOSType(t *testing.T) {
	seen := map[string]bool{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			writePVEResponse(t, w, []pveNode{{Node: "pve-a"}})
		case "/api2/json/nodes/pve-a/version":
			writePVEResponse(t, w, pveNodeVersion{Version: "8.4.1", Release: "1", RepoID: "a2b3c4d5"})
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
		"/api2/json/nodes/pve-a/qemu/101/config",
		"/api2/json/nodes/pve-a/lxc/202/config",
	} {
		if !seen[path] {
			t.Fatalf("expected config endpoint %s to be queried", path)
		}
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
			name:       "running high memory is unhealthy",
			resource:   pveResource{Status: "running", Mem: 96, MaxMem: 100},
			wantHealth: model.HealthUnhealthy,
			wantDetail: "High memory usage: 96% of 100 B",
		},
		{
			name:       "running high disk is unhealthy",
			resource:   pveResource{Status: "running", Disk: 97, MaxDisk: 100},
			wantHealth: model.HealthUnhealthy,
			wantDetail: "High disk usage: 97% of 100 B",
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
