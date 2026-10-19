package tui

import (
	"strings"
	"testing"
)

// readCard builds a finished read card carrying an envelope, the way
// toolCard.complete records the worker's answer.
func readCard(path, env string) *toolCard {
	card := &toolCard{name: "read", input: map[string]any{"path": path}, state: cardRunning}
	card.complete(true, "package a", map[string]any{"envelope": env})
	return card
}

// A group whose calls all ran the same way says so once, on its header.
func TestExploreGroupMarksAUniformEnvelope(t *testing.T) {
	g := exploreGroup{cards: []*toolCard{
		readCard("a.go", envContained),
		readCard("b.go", envContained),
	}}
	out := stripANSI(g.render(DefaultTheme(), 100))
	if !strings.Contains(out, "Explored ·sandbox") {
		t.Fatalf("header = %q, want the sandbox marker on it", out)
	}
	if strings.Count(out, "·sandbox") != 1 {
		t.Errorf("the marker belongs on the header, not every row:\n%s", out)
	}
}

// The exception is the interesting case: a group with one unconfined call
// cannot make a claim on the header, so the rows carry their own answer.
func TestExploreGroupMarksAMixedEnvelopePerRow(t *testing.T) {
	g := exploreGroup{cards: []*toolCard{
		readCard("a.go", envContained),
		readCard("b.go", envUnconfined),
	}}
	out := stripANSI(g.render(DefaultTheme(), 100))
	if strings.Contains(out, "Explored ·") {
		t.Errorf("a mixed group must not claim one envelope on the header:\n%s", out)
	}
	if !strings.Contains(out, "Read a.go ·sandbox") {
		t.Errorf("the contained read should carry its own marker:\n%s", out)
	}
	if !strings.Contains(out, "Read b.go ·unconfined") {
		t.Errorf("the unconfined read should carry its own marker:\n%s", out)
	}
}

// An unfinished call has not reported an envelope yet, and a group that has
// reported nothing claims nothing.
func TestExploreGroupWithoutEnvelopesClaimsNothing(t *testing.T) {
	g := exploreGroup{cards: []*toolCard{
		{name: "read", input: map[string]any{"path": "a.go"}, state: cardSuccess},
	}}
	out := stripANSI(g.render(DefaultTheme(), 100))
	if strings.Contains(out, "·") {
		t.Errorf("no envelope was reported, so nothing should be claimed:\n%s", out)
	}
}
