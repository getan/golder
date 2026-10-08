package tui

// Tests for the exploring cell (explore.go): consecutive read-only calls
// coalesce into one block, a call with side effects ends the run, consecutive
// reads merge onto a single line, and expanding the group reveals every call in
// full.

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/judge"
)

func exploreCard(id, name string, input map[string]any, state cardState) *toolCard {
	return &toolCard{id: id, name: name, input: input, state: state}
}

// TestExploreGroupCoalescesReads: consecutive reads become ONE block with a
// single merged `Read a, b` line (duplicate paths collapse), and the trailing
// non-exploration call keeps its own card.
func TestExploreGroupCoalescesReads(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(120, 40)
	tr.addToolCard(exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess))
	tr.addToolCard(exploreCard("2", "read", map[string]any{"path": "b.go"}, cardSuccess))
	tr.addToolCard(exploreCard("3", "read", map[string]any{"path": "a.go"}, cardSuccess))
	tr.addToolCard(exploreCard("4", "bash", map[string]any{"command": "go test ./..."}, cardSuccess))

	var groups, cards int
	for _, b := range tr.blocks {
		if b.group != nil {
			groups++
		}
		if b.card != nil {
			cards++
		}
	}
	if groups != 1 {
		t.Fatalf("explore groups = %d, want 1", groups)
	}
	if cards != 1 {
		t.Fatalf("standalone cards = %d, want 1 (the bash call)", cards)
	}
	if got := len(tr.blocks[0].group.cards); got != 3 {
		t.Fatalf("grouped reads = %d, want 3", got)
	}

	view := stripANSI(strings.Join(tr.contentLines(), "\n"))
	if !strings.Contains(view, "• Explored") {
		t.Errorf("group must render an Explored header:\n%s", view)
	}
	if !strings.Contains(view, "Read a.go, b.go") {
		t.Errorf("consecutive reads must merge onto one line:\n%s", view)
	}
	if strings.Count(view, "• Explored") != 1 {
		t.Errorf("one run of reads must render one cell:\n%s", view)
	}
}

// TestExploreGroupSplitsAroundSideEffects: a bash call between two read runs
// ends the first group, so the second run opens a new cell instead of merging
// across the side effect (the ordering the transcript must keep showing).
func TestExploreGroupSplitsAroundSideEffects(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(120, 40)
	tr.addToolCard(exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess))
	tr.addToolCard(exploreCard("2", "bash", map[string]any{"command": "git status"}, cardSuccess))
	tr.addToolCard(exploreCard("3", "grep", map[string]any{"pattern": "TODO", "path": "internal"}, cardSuccess))

	var groups int
	for _, b := range tr.blocks {
		if b.group != nil {
			groups++
		}
	}
	if groups != 2 {
		t.Fatalf("explore groups = %d, want 2 (split by the bash call)", groups)
	}
	view := stripANSI(strings.Join(tr.contentLines(), "\n"))
	if !strings.Contains(view, "Search TODO in internal") {
		t.Errorf("grep line missing from the group summary:\n%s", view)
	}
}

// TestExploreGroupSummaryVerbs pins the per-tool wording: Read for files, List
// for directories, Search for patterns (with their scope), Find for name globs.
func TestExploreGroupSummaryVerbs(t *testing.T) {
	g := exploreGroup{cards: []*toolCard{
		exploreCard("1", "read", map[string]any{"path": "main.go"}, cardSuccess),
		exploreCard("2", "ls", map[string]any{"path": "internal"}, cardSuccess),
		exploreCard("3", "ls", nil, cardSuccess),
		exploreCard("4", "grep", map[string]any{"pattern": "Verify", "glob": "*.go"}, cardSuccess),
		exploreCard("5", "find", map[string]any{"glob": "*_test.go", "path": "internal"}, cardSuccess),
	}}
	got := g.summaryLines()
	want := []string{
		"Read main.go",
		"List internal",
		"List .",
		"Search Verify in *.go",
		"Find *_test.go in internal",
	}
	if len(got) != len(want) {
		t.Fatalf("summary lines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExploreGroupStates pins the aggregate header: Exploring while any call
// runs, Explored once all are done, and the failure color when one failed
// (checked through the stable() commit signal too).
func TestExploreGroupStates(t *testing.T) {
	running := exploreGroup{cards: []*toolCard{
		exploreCard("1", "read", nil, cardSuccess),
		exploreCard("2", "read", nil, cardRunning),
	}}
	if !running.running() || running.stable() {
		t.Error("a group with a running call must report running and unstable")
	}
	if !strings.Contains(stripANSI(running.render(DefaultTheme(), 80)), "Exploring") {
		t.Error("running group must render Exploring")
	}

	done := exploreGroup{cards: []*toolCard{
		exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess),
		exploreCard("2", "read", map[string]any{"path": "b.go"}, cardWarn),
	}}
	if done.running() || !done.stable() {
		t.Error("a finished group must report stable and not running")
	}
	if !done.failed() {
		t.Error("a group with a failed call must report failed")
	}
	if !strings.Contains(stripANSI(done.render(DefaultTheme(), 80)), "Explored") {
		t.Error("finished group must render Explored")
	}
}

// TestExploreGroupExpandRevealsCards: expanding the group renders every member
// call in full (its own headline plus arguments), so no output is ever
// unreachable behind the summary.
func TestExploreGroupExpandRevealsCards(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(120, 40)
	c1 := exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess)
	c2 := exploreCard("2", "find", map[string]any{"glob": "*_test.go", "path": "internal"}, cardSuccess)
	tr.addToolCard(c1)
	tr.addToolCard(c2)
	tr.reflow()

	if got := stripANSI(strings.Join(tr.contentLines(), "\n")); strings.Contains(got, "Ran read") {
		t.Errorf("collapsed group must not render member headlines:\n%s", got)
	}
	tr.toggleGroupExpanded(tr.blocks[0].group)
	tr.reflow()
	got := stripANSI(strings.Join(tr.contentLines(), "\n"))
	for _, want := range []string{"Ran read", "a.go", "Ran find", "*_test.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("expanded group missing %q:\n%s", want, got)
		}
	}
}

// TestExploreGroupCtrlTTogglesGroup: Ctrl+T targets the newest card; when that
// card lives in an exploration group, the group is what opens (the card itself
// has no rendered cell of its own).
func TestExploreGroupCtrlTTogglesGroup(t *testing.T) {
	m := NewModel(Options{})
	m.transcript.setSize(120, 40)
	card := exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess)
	m.toolCards["1"] = card
	m.lastToolCard = card
	m.transcript.addToolCard(card)
	m.transcript.reflow()

	got, _ := m.Update(ctrlKey('t'))
	m = got.(Model)
	if g := m.transcript.groupOf(card); g == nil || !g.expanded {
		t.Fatal("Ctrl+T on a card inside an exploration group must expand the group")
	}
	got, _ = m.Update(ctrlKey('t'))
	m = got.(Model)
	if g := m.transcript.groupOf(card); g == nil || g.expanded {
		t.Fatal("a second Ctrl+T must collapse the group again")
	}
}

// TestExploreGroupClickRouting: a click inside the group's rendered rows maps
// to the group (not to a member card), and toggling it re-renders expanded.
func TestExploreGroupClickRouting(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(120, 40)
	tr.addToolCard(exploreCard("1", "read", map[string]any{"path": "a.go"}, cardSuccess))
	tr.addToolCard(exploreCard("2", "read", map[string]any{"path": "b.go"}, cardSuccess))
	tr.reflow()

	span, ok := tr.groupSpans[tr.blocks[0].group]
	if !ok {
		t.Fatal("renderContent must record the group's line span for click routing")
	}
	g := tr.exploreGroupAt(span[0])
	if g == nil {
		t.Fatalf("nodeAt(%d) found no group", span[0])
	}
	if c := tr.toolCardAt(span[0]); c != nil {
		t.Errorf("group rows must route to the group, not to member card %q", c.name)
	}
	tr.toggleGroupExpanded(g)
	tr.reflow()
	if !strings.Contains(stripANSI(strings.Join(tr.contentLines(), "\n")), "Ran read") {
		t.Error("toggled group must render the expanded member cards")
	}
}

// TestExploreGroupNoteAttaches: a gate verdict published for a call inside a
// group must still find its card and render under the call once the group is
// expanded.
func TestExploreGroupNoteAttaches(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(120, 40)
	card := exploreCard("call-1", "read", map[string]any{"path": "secret"}, cardSuccess)
	tr.addToolCard(card)
	tr.addReviewNote(judge.Note{
		ToolCallID: "call-1",
		Tool:       "read",
		Kind:       judge.NoteDenied,
		Level:      judge.Deny,
		Summary:    "reads credential material",
	})

	if len(card.notes) != 1 {
		t.Fatalf("card notes = %d, want 1 (the verdict must find its card inside the group)", len(card.notes))
	}
}
