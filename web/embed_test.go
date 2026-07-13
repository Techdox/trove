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
