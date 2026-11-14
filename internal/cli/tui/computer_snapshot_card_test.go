package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComputerSnapshotCardFoldAndExpand(t *testing.T) {
	card := toolCard{name: "computer", input: map[string]any{"action": "snapshot"}, state: cardSuccess}
	card.complete(true, strings.Repeat("窗口数据", 1500)+" END-OF-SNAPSHOT", nil)
	preview := stripTCardANSI(card.render(DefaultTheme(), 60))
	if lines := strings.Split(preview, "\n"); len(lines) != 5 || strings.Contains(preview, "END-OF-SNAPSHOT") || !strings.Contains(preview, "ctrl+t or click to expand") {
		t.Fatalf("snapshot preview should have headline, three rows and hint: %q", preview)
	}
	card.toggleExpanded()
	if expanded := stripTCardANSI(card.render(DefaultTheme(), 60)); !strings.Contains(expanded, "END-OF-SNAPSHOT") || strings.Contains(expanded, "click to expand") {
		t.Fatalf("expanded snapshot lost output: %q", expanded)
	}
	card.toggleExpanded()
	if got := stripTCardANSI(card.render(DefaultTheme(), 60)); got != preview {
		t.Fatalf("collapse did not restore preview: %q", got)
	}
}

func TestComputerSnapshotCardMouseAndKeyboard(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m = apply(t, m, toolStartMsg{id: "snap", name: "computer", input: map[string]any{"action": "snapshot"}})
	m = apply(t, m, toolEndMsg{id: "snap", ok: true, result: strings.Repeat("data", 100) + " END"})
	card := m.toolCards["snap"]
	if card == nil || card.expanded {
		t.Fatal("snapshot must start collapsed")
	}
	m = apply(t, m, ctrlKey('t'))
	if !card.expanded {
		t.Fatal("Ctrl+T did not expand snapshot")
	}
	m = apply(t, m, ctrlKey('t'))
	if card.expanded {
		t.Fatal("Ctrl+T did not collapse snapshot")
	}
	m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if !card.expanded {
		t.Fatal("click did not expand snapshot")
	}
	m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if card.expanded {
		t.Fatal("click did not collapse snapshot")
	}
}

func TestComputerNonSnapshotDoesNotUseSnapshotPreview(t *testing.T) {
	card := toolCard{name: "computer", input: map[string]any{"action": "click"}}
	if card.hasBoundedPreview() {
		t.Fatal("non-snapshot action matched snapshot renderer")
	}
}
