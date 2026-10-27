package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipRenderTextBoundsAHugeLine(t *testing.T) {
	huge := strings.Repeat("a", 1<<20)
	out := clipRenderText(huge)
	if len(out) > renderMaxTextBytes+len(renderTruncated)+2 {
		t.Fatalf("clipped text is %d bytes", len(out))
	}
	if !strings.Contains(out, renderTruncated) {
		t.Fatal("no truncation notice")
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > renderMaxLineBytes+len(" …") {
			t.Fatalf("line is %d bytes", len(line))
		}
	}
	if !utf8.ValidString(out) {
		t.Fatal("clipping split a rune")
	}
}

func TestClipRenderTextLeavesSmallTextAlone(t *testing.T) {
	const text = "short\nlines\n"
	if got := clipRenderText(text); got != text {
		t.Fatalf("small text changed: %q", got)
	}
}

func TestRenderDiffBoundsAOneLineBuildArtifact(t *testing.T) {
	diff := "--- a/out.js\n+++ b/out.js\n@@ -1 +1 @@\n+" + strings.Repeat("a", 122_000)
	out := renderDiff(DefaultTheme(), diff, 80)
	if !strings.Contains(out, "…") {
		t.Fatal("the huge diff line was not marked as truncated")
	}
	if len(out) > 256<<10 {
		t.Fatalf("rendered diff is %d bytes", len(out))
	}
}

func TestToolCardBoundsAHugeResponse(t *testing.T) {
	card := toolCard{
		name:     "bash",
		state:    cardSuccess,
		expanded: true,
		response: []respNode{{text: strings.Repeat("x", 1<<20)}},
	}
	out := card.renderForced(DefaultTheme(), 80, true)
	if !strings.Contains(out, "…") {
		t.Fatal("the huge response was not marked as truncated")
	}
	if len(out) > 256<<10 {
		t.Fatalf("rendered card is %d bytes", len(out))
	}
}
