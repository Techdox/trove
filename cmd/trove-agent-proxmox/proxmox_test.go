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
		case "/api2/json/cluster/resources":
			if got := r.URL.Query().Get("type"); got != "vm" {
				t.Fatalf("resources type query = %q, want vm", got)
			}
			writePVEResponse(t, w, []pveResource{
				{Type: "qemu", VMID: 101, Name: "winbox", Status: "running", Node: "pve-a"},
				{Type: "lxc", VMID: 202, Name: "debian-ct", Status: "running", Node: "pve-a"},
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
	services := snaps[0].Services
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ExternalID < services[j].ExternalID })

	assertServiceImage(t, services[0], "lxc/202", model.KindLXC, "Debian", "debian")
	assertServiceImage(t, services[1], "qemu/101", model.KindVM, "Windows 11", "win11")

	for _, path := range []string{
		"/api2/json/nodes/pve-a/qemu/101/config",
		"/api2/json/nodes/pve-a/lxc/202/config",
	} {
		if !seen[path] {
			t.Fatalf("expected config endpoint %s to be queried", path)
		}
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
