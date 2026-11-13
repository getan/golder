package tui

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/ui"
)

func TestToolPreviewPrettyPrintsJSONAndWrapsLongValues(t *testing.T) {
	card := toolCard{name: "browser", input: map[string]any{"action": "snapshot"}, expanded: true}
	card.complete(true, `{"nodes":[{"text":"`+strings.Repeat("中文A", 80)+`"}],"count":9007199254740993}`, nil)
	if len(card.response) < 5 {
		t.Fatal("JSON remained a single response line")
	}
	for _, width := range []int{32, 60, 100} {
		text := stripTCardANSI(card.render(DefaultTheme(), width))
		if !strings.Contains(text, `"nodes": [`) || !strings.Contains(strings.Join(strings.Fields(text), ""), `9007199254740993`) {
			t.Fatalf("formatted JSON lost data: %q", text)
		}
		for _, line := range strings.Split(text, "\n") {
			if ui.Width(line) > width {
				t.Fatalf("preview overflowed %d columns: %q", width, line)
			}
		}
	}
}

func TestReadToolPreviewFormatsNumberedJSON(t *testing.T) {
	card := toolCard{name: "read", input: map[string]any{"path": "fixture.json"}, expanded: true}
	card.complete(true, "     1\t{\"name\":\"golder\",\"items\":[1,2]}\n", nil)
	text := stripTCardANSI(card.render(DefaultTheme(), 100))
	for _, want := range []string{`"name": "golder"`, `"items": [`, `1`, `2`} {
		if !strings.Contains(text, want) {
			t.Fatalf("read preview is missing %q: %q", want, text)
		}
	}
	if strings.Count(text, "\n") < 6 {
		t.Fatalf("read JSON stayed on one line: %q", text)
	}
}
