package agenttool

import (
	"strings"
	"testing"
)

func TestUnifiedDiffLimitedStopsAtBudget(t *testing.T) {
	old := strings.Repeat("old line\n", 200)
	new := strings.Repeat("new line\n", 200)
	const budget = 128
	got := unifiedDiffLimited("x.txt", old, new, budget)
	if !strings.Contains(got, "diff truncated") {
		t.Fatalf("capped diff missing marker: %q", got)
	}
	if len(got) > budget+128 {
		t.Fatalf("capped diff grew to %d bytes (budget %d)", len(got), budget)
	}
	if full := unifiedDiff("x.txt", old, new); len(full) <= budget || !strings.Contains(full, "-old line") {
		t.Fatalf("uncapped diff changed: %d bytes", len(full))
	}
}
