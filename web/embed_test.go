package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestDashboardAttentionHierarchyIsEmbedded(t *testing.T) {
	t.Parallel()

	assets := FS()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}

	last := -1
	for _, heading := range []string{
		"Needs attention",
		"Infrastructure summary",
		"Service catalogue",
		"Recent changes",
	} {
		pos := strings.Index(string(index), heading)
		if pos == -1 {
			t.Errorf("dashboard is missing %q", heading)
			continue
		}
		if pos < last {
			t.Errorf("dashboard heading %q is out of hierarchy order", heading)
		}
		last = pos
	}

	app, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}
	for _, marker := range []string{
		"function attentionItems()",
		"function showAttention(key)",
		"function focusInvestigationTarget(id)",
		"Swipe or scroll horizontally to see all service details",
		`const STOPPED_STATES = new Set(["exited", "dead", "failed", "stopped"])`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard attention behaviour is missing %q", marker)
		}
	}

	for _, marker := range []string{
		`id="infrastructure-title" tabindex="-1"`,
		`id="inventory-title" tabindex="-1"`,
	} {
		if !strings.Contains(string(index), marker) {
			t.Errorf("dashboard focus target is missing %q", marker)
		}
	}
}

func TestDashboardBrandAssetsAreEmbedded(t *testing.T) {
	t.Parallel()

	assets := FS()
	for _, name := range []string{
		"favicon.ico",
		"favicon.svg",
		"trove-icon-180.png",
		"trove-icon-192.png",
		"trove-icon-512.png",
		"trove-mark.svg",
		"trove-wordmark.svg",
	} {
		info, err := fs.Stat(assets, name)
		if err != nil {
			t.Errorf("embedded brand asset %q: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("embedded brand asset %q is empty", name)
		}
	}

	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	for _, marker := range []string{
		`src="trove-mark.svg" alt=""`,
		`src="trove-wordmark.svg" alt="Trove"`,
	} {
		if !strings.Contains(string(index), marker) {
			t.Errorf("dashboard brand markup is missing %q", marker)
		}
	}

	mark, err := fs.ReadFile(assets, "trove-mark.svg")
	if err != nil {
		t.Fatalf("read embedded mark: %v", err)
	}
	for _, marker := range []string{
		`fill="#7657f6"`,
		`fill-rule="evenodd"`,
		`indexed catalogue card`,
	} {
		if !strings.Contains(string(mark), marker) {
			t.Errorf("dashboard mark is missing Open Index marker %q", marker)
		}
	}

	wordmark, err := fs.ReadFile(assets, "trove-wordmark.svg")
	if err != nil {
		t.Fatalf("read embedded wordmark: %v", err)
	}
	if strings.Contains(string(wordmark), "<text") {
		t.Error("dashboard wordmark must remain path-based, not runtime text")
	}
}

func TestDashboardRemovedAttentionFiltersOnlyRemovedServices(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}

	for _, marker := range []string{
		"removedOnly: false",
		`if (state.removedOnly && s.state !== "removed") return false;`,
		`if (!state.removedOnly && !state.showRemoved && s.state === "removed") return false;`,
		`if (key === "removed") state.removedOnly = true;`,
		`const remLabel = state.removedOnly ? "removed only" : "removed";`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard removed-service triage is missing %q", marker)
		}
	}
}

func TestDashboardShowsIndependentHostLiveness(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}

	for _, marker := range []string{
		`if (h.status === "stale" && h.agent_status !== "stale" && h.agent_status !== "offline") c.staleHosts++;`,
		`if (h.status === "offline" && h.agent_status !== "offline") c.offlineHosts++;`,
		`item("offline-hosts"`,
		`item("stale-hosts"`,
		`["offline-hosts", "stale-hosts", "critical-hosts", "warning-hosts"].includes(key)`,
		"last report ${esc(relTime(h.last_seen_at))}",
		"`reporting ${st}`",
		`case "host":`,
		`e.kind === "agent" || e.kind === "host"`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard host liveness is missing %q", marker)
		}
	}
}

func TestDashboardShowsHostConditionAndMetrics(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}

	for _, marker := range []string{
		"function hostMetricItems(metrics)",
		"function hostMetricsHTML(metrics)",
		`item("critical-hosts"`,
		`item("warning-hosts"`,
		"`condition ${condition}`",
		"hostMetricsHTML(h.metrics)",
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard host metrics are missing %q", marker)
		}
	}
}
