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
		`<caption class="visually-hidden">Services reported for`,
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

func TestDashboardAccessibilityContract(t *testing.T) {
	t.Parallel()

	assets := FS()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	for _, marker := range []string{
		`class="skip-link" href="#main-content"`,
		`<main id="main-content" tabindex="-1">`,
		`role="status" aria-live="polite"`,
		`id="error" role="alert"`,
		`role="search" aria-label="Filter service catalogue"`,
		`role="dialog" aria-modal="true"`,
		`aria-labelledby="drawer-title" tabindex="-1"`,
	} {
		if !strings.Contains(string(index), marker) {
			t.Errorf("dashboard accessibility markup is missing %q", marker)
		}
	}

	app, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}
	for _, marker := range []string{
		`class="service-details" data-service-details aria-haspopup="dialog"`,
		`aria-controls="drawer" aria-label="View details for`,
		`<h2 class="d-name" id="drawer-title">`,
		"function setPageInert(inert)",
		"element.inert = inert;",
		"function drawerFocusables()",
		"function trapDrawerFocus(event)",
		`if (e.key === "Tab" && (state.drawerKey || state.hostDrawerKey))`,
		`const restoreDrawerFocus = !$("drawer").hidden && $("drawer").contains(document.activeElement);`,
		`?.querySelector("[data-service-details]")?.focus`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard accessible interaction is missing %q", marker)
		}
	}
	if strings.Contains(string(app), `<tr class="${cls}" tabindex="0"`) {
		t.Error("service rows must use their explicit button instead of a non-semantic row tab stop")
	}
}

func TestDashboardMobileStatusDoesNotRequireHorizontalScrolling(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}
	for _, marker := range []string{
		`class="badgecell state-cell" data-label="State"`,
		`class="badgecell health-cell" data-label="Health"`,
		`class="badgecell fresh-cell" data-label="Freshness"`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("mobile service status markup is missing %q", marker)
		}
	}

	styles, err := fs.ReadFile(FS(), "styles.css")
	if err != nil {
		t.Fatalf("read embedded styles: %v", err)
	}
	for _, marker := range []string{
		`.host-body { overflow-x: visible; }`,
		`table { min-width: 0; table-layout: auto; }`,
		`grid-template-areas:`,
		`"service service service"`,
		`"state health fresh"`,
		`tbody tr[data-ext] td.state-cell { grid-area: state; }`,
		`tbody tr[data-ext] td.health-cell { grid-area: health; }`,
	} {
		if !strings.Contains(string(styles), marker) {
			t.Errorf("mobile status layout is missing %q", marker)
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
		"function hostMetricRows(metrics)",
		"function hostMetricsHTML(metrics)",
		"function findHost(key)",
		"function findAgent(name)",
		"function hostMetaLabel(key)",
		"function hostMetricsNoticeHTML(host)",
		"function agentVersionLabel(version)",
		"function missingHostMetricsHTML(host, agent)",
		"function openHostDrawer(key)",
		`item("critical-hosts"`,
		`item("warning-hosts"`,
		"`condition ${condition}`",
		"hostMetricsHTML(h.metrics)",
		"data-host-details",
		"View host stats",
		"No host metrics reported",
		"agent version",
		"docker.host_metrics",
		"kubernetes.metrics_api",
		"state.drawerKey || state.hostDrawerKey",
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard host metrics are missing %q", marker)
		}
	}

	for _, duplicate := range []string{
		`<span class="d-detail-label">Snapshot</span>`,
		"No host resource metrics were included in the latest report.",
		"Waiting for a compatible agent to report CPU, load, memory, disk, and uptime.",
	} {
		if strings.Contains(string(app), duplicate) {
			t.Errorf("dashboard host drawer repeats its metrics state with %q", duplicate)
		}
	}
}

func TestDashboardHostNavigationAndPlatformMarks(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}

	for _, marker := range []string{
		"function platformIdentity(platform)",
		"function platformIconHTML(platform)",
		"function openAgentDestination(name)",
		"function openHostDrawerFromAgent(key, returnAgent)",
		`data-agent-destination="${esc(a.name)}"`,
		`class="host-name" data-host-details`,
		`openHostDrawerFromAgent(hostKey(hosts[0]), name)`,
		`state.q = name;`,
		`case "docker":`,
		`case "proxmox":`,
		`case "kubernetes":`,
		`case "linux":`,
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard host navigation is missing %q", marker)
		}
	}
}

func TestDashboardEnterShortcutDefersToNativeControls(t *testing.T) {
	t.Parallel()

	app, err := fs.ReadFile(FS(), "app.js")
	if err != nil {
		t.Fatalf("read embedded app: %v", err)
	}

	source := string(app)
	enter := strings.Index(source, `if (e.key === "Enter") {`)
	if enter == -1 {
		t.Fatal("dashboard Enter shortcut is missing")
	}
	shortcut := source[enter:]
	guard := strings.Index(shortcut, `if (e.target.closest?.("button, a, [role='button'], [role='link']")) return;`)
	fallback := strings.Index(shortcut, `if (state.cursorKey) openDrawer(state.cursorKey);`)
	if guard == -1 {
		t.Fatal("dashboard Enter shortcut does not defer to native controls")
	}
	if fallback == -1 {
		t.Fatal("dashboard Enter shortcut cursor fallback is missing")
	}
	if guard > fallback {
		t.Fatal("dashboard Enter shortcut checks the stale row cursor before native controls")
	}
}
