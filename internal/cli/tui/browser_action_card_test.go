package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestBrowserActionCardsBoundWrappedOutput(t *testing.T) {
	for _, action := range []string{"navigate", "network", "tab_select"} {
		for _, ok := range []bool{true, false} {
			t.Run(action+map[bool]string{true: "/success", false: "/error"}[ok], func(t *testing.T) {
				card := toolCard{name: "browser", input: map[string]any{"action": action}, state: cardSuccess}
				card.complete(ok, strings.Repeat("浏览器输出", 1200)+" END-OF-RESULT", nil)
				preview := stripTCardANSI(card.render(DefaultTheme(), 80))
				if lines := strings.Split(preview, "\n"); len(lines) != 5 || strings.Contains(preview, "END-OF-RESULT") || !strings.Contains(preview, "… (ctrl+t or click to expand)") {
					t.Fatalf("want headline, three physical rows and hint: %q", preview)
				}
				card.toggleExpanded()
				expanded := stripTCardANSI(card.render(DefaultTheme(), 80))
				if !strings.Contains(expanded, "END-OF-RESULT") || strings.Contains(expanded, "click to expand") {
					t.Fatalf("expanded result lost output: %q", expanded)
				}
				card.toggleExpanded()
				if got := stripTCardANSI(card.render(DefaultTheme(), 80)); got != preview {
					t.Fatalf("collapse did not restore preview: %q", got)
				}
			})
		}
	}
}

func TestBrowserActionCardsShortOutputAndScope(t *testing.T) {
	for _, action := range []string{"navigate", "network", "tab_select"} {
		card := toolCard{name: "browser", input: map[string]any{"action": action}, state: cardSuccess}
		card.complete(true, "first\nsecond\nthird", nil)
		got := stripTCardANSI(card.render(DefaultTheme(), 80))
		if !strings.Contains(got, "third") || strings.Contains(got, "click to expand") {
			t.Errorf("%s: short output should remain visible: %q", action, got)
		}
	}
	for _, card := range []toolCard{
		{name: "browser", input: map[string]any{"action": "click"}},
		{name: "computer", input: map[string]any{"action": "navigate"}},
		{name: "network", input: map[string]any{"action": "network"}},
	} {
		if card.hasBoundedPreview() {
			t.Errorf("unrelated action matched bounded browser renderer: %+v", card)
		}
	}
}

func TestBrowserActionCardsMouseAndKeyboard(t *testing.T) {
	for _, action := range []string{"navigate", "network", "tab_select"} {
		t.Run(action, func(t *testing.T) {
			m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
			m = apply(t, m, toolStartMsg{id: "browser", name: "browser", input: map[string]any{"action": action}})
			m = apply(t, m, toolEndMsg{id: "browser", ok: true, result: strings.Repeat("data", 100) + " END"})
			card := m.toolCards["browser"]
			if card == nil || card.expanded {
				t.Fatal("browser action must start collapsed")
			}
			m = apply(t, m, ctrlKey('t'))
			if !card.expanded {
				t.Fatal("Ctrl+T did not expand the card")
			}
			m = apply(t, m, ctrlKey('t'))
			if card.expanded {
				t.Fatal("Ctrl+T did not collapse the card")
			}
			m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
			m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
			if !card.expanded {
				t.Fatal("click did not expand the card")
			}
			m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
			m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
			if card.expanded {
				t.Fatal("click did not collapse the card")
			}
		})
	}
}
