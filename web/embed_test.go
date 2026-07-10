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
		"Recent changes",
		"Infrastructure summary",
		"Service catalogue",
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
		"STOPPED_STATES",
	} {
		if !strings.Contains(string(app), marker) {
			t.Errorf("dashboard attention behaviour is missing %q", marker)
		}
	}
}
