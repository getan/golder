package tui

import (
	"strings"
	"testing"
)

func TestBrowserSnapshotCardPreviewBoundsWrappedRows(t *testing.T) {
	card := toolCard{name: "browser", input: map[string]any{"action": "snapshot"}, state: cardSuccess}
	card.complete(true, strings.Repeat("abcdefghij", 80)+" END-OF-SNAPSHOT", nil)
	preview := stripTCardANSI(card.render(DefaultTheme(), 50))
	if strings.Contains(preview, "END-OF-SNAPSHOT") || !strings.Contains(preview, "ctrl+t or click to expand") {
		t.Fatalf("snapshot preview did not fold: %q", preview)
	}
	if lines := strings.Split(preview, "\n"); len(lines) != 5 {
		t.Fatalf("want headline + three physical rows + hint, got %d: %q", len(lines), preview)
	}
	card.toggleExpanded()
	expanded := stripTCardANSI(card.render(DefaultTheme(), 50))
	if !strings.Contains(expanded, "END-OF-SNAPSHOT") || strings.Contains(expanded, "click to expand") {
		t.Fatalf("expanded snapshot lost output: %q", expanded)
	}
	card.toggleExpanded()
	if got := stripTCardANSI(card.render(DefaultTheme(), 50)); got != preview {
		t.Fatalf("collapse did not restore preview: %q", got)
	}
}

func TestBrowserSnapshotCardShortOutput(t *testing.T) {
	card := toolCard{name: "browser", input: map[string]any{"action": "snapshot"}, state: cardSuccess}
	card.complete(true, "first\nsecond\nthird", nil)
	got := stripTCardANSI(card.render(DefaultTheme(), 80))
	if !strings.Contains(got, "third") || strings.Contains(got, "click to expand") {
		t.Fatalf("short snapshot should not be truncated: %q", got)
	}
}

func TestBrowserSnapshotExpandedLongJSON(t *testing.T) {
	card := toolCard{name: "browser", input: map[string]any{"action": "snapshot"}, state: cardSuccess, expanded: true}
	card.complete(true, strings.Repeat("权限", 1500)+" END-MARKER", nil)
	got := stripTCardANSI(card.render(DefaultTheme(), 120))
	if !strings.Contains(got, "END-MARKER") || strings.Contains(got, renderTruncated) {
		t.Fatal("expanded snapshot silently clipped a long JSON line")
	}
	nodes := snapshotDetailNodes([]respNode{{text: strings.Repeat("a", renderMaxTextBytes+10)}})
	if len(nodes) == 0 || nodes[len(nodes)-1].text != renderTruncated {
		t.Fatal("oversized snapshot did not report the display budget")
	}
}
