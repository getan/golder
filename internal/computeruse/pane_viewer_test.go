package computeruse

import (
	"strings"
	"testing"
)

func TestPaneFillInputKeepsUnicodeAndDoesNotEchoText(t *testing.T) {
	line := "fill @e2 世界  two"
	ref, text, ok := paneFillArguments(line)
	if !ok || ref != "@e2" || text != "世界  two" {
		t.Fatalf("fill arguments: ref=%q text=%q ok=%t", ref, text, ok)
	}
	if display := paneInputDisplay(line); strings.Contains(display, "世界") || strings.Contains(display, "two") || !strings.Contains(display, "@e2") {
		t.Fatalf("typed value appeared in pane output: %q", display)
	}
	if _, _, ok := paneFillArguments("fill @e2"); ok {
		t.Fatal("missing text would silently clear a field")
	}
}
