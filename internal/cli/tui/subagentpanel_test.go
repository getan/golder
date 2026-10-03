package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/ui"
)

// TestSubagentPanelLifecycle exercises the ordered add/update/remove set: rows
// keep insertion order, a progress refreshes activity/tokens, a progress for an
// unknown id adds a row (late/out-of-order safe), and remove drops exactly one
// row (a no-op for an absent id).
func TestSubagentPanelLifecycle(t *testing.T) {
	now := time.Now()
	var p subagentPanel

	p.add("a", "task A", now)
	p.add("b", "task B", now)
	if got := p.active(); got != 2 {
		t.Fatalf("active after two adds = %d, want 2", got)
	}
	if p.order[0] != "a" || p.order[1] != "b" {
		t.Errorf("order = %v, want [a b]", p.order)
	}

	p.update("a", "task A", "Editing", 120, now)
	if row := p.byID["a"]; row.activity != "Editing" || row.tokens != 120 {
		t.Errorf("row a after update = %+v, want activity=Editing tokens=120", row)
	}

	// A progress for an id that never started adds the row (SPEC 5.4).
	p.update("c", "task C", "Reading", 0, now)
	if got := p.active(); got != 3 {
		t.Fatalf("active after late progress = %d, want 3", got)
	}
	if p.order[2] != "c" {
		t.Errorf("order = %v, want c appended last", p.order)
	}

	// Removing a middle row preserves the order of the rest.
	p.remove("a")
	if got := p.active(); got != 2 {
		t.Fatalf("active after remove = %d, want 2", got)
	}
	if p.order[0] != "b" || p.order[1] != "c" {
		t.Errorf("order after remove(a) = %v, want [b c]", p.order)
	}

	// Removing an absent id is a no-op.
	p.remove("zzz")
	if got := p.active(); got != 2 {
		t.Errorf("active after remove(absent) = %d, want 2", got)
	}
}

// TestSubagentPanelEmptyView verifies an empty panel renders nothing — zero
// lines, zero height — so the single-run layout is untouched.
func TestSubagentPanelEmptyView(t *testing.T) {
	var p subagentPanel
	if got := p.view(DefaultTheme(), 80, time.Now()); got != "" {
		t.Errorf("empty panel view = %q, want empty", got)
	}
	// A non-empty panel with a non-positive width also renders nothing.
	p.add("a", "task", time.Now())
	if got := p.view(DefaultTheme(), 0, time.Now()); got != "" {
		t.Errorf("zero-width view = %q, want empty", got)
	}
}

// TestSubagentPanelViewLines verifies one line per active sub-agent, each
// carrying the description, activity, and token stat.
func TestSubagentPanelViewLines(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "build parser", now)
	p.update("a", "build parser", "Editing", 1200, now)
	p.add("b", "run tests", now)
	p.update("b", "run tests", "Running bash", 0, now)

	view := p.view(DefaultTheme(), 200, now.Add(65*time.Second))
	lines := strings.Split(view, "\n")
	if len(lines) != 2 {
		t.Fatalf("view has %d lines, want 2: %q", len(lines), view)
	}
	if !strings.Contains(lines[0], "build parser") || !strings.Contains(lines[0], "Editing") {
		t.Errorf("line[0] = %q, want desc + activity", lines[0])
	}
	if !strings.Contains(lines[0], "1m 5s") {
		t.Errorf("line[0] = %q, want elapsed 1m 5s", lines[0])
	}
	if !strings.Contains(lines[0], "1,200") {
		t.Errorf("line[0] = %q, want token stat 1,200", lines[0])
	}
	if !strings.Contains(lines[1], "run tests") || !strings.Contains(lines[1], "Running bash") {
		t.Errorf("line[1] = %q, want desc + activity", lines[1])
	}
	// A zero token estimate omits the ↓ stat.
	if strings.Contains(lines[1], "↓") {
		t.Errorf("line[1] = %q, should omit ↓ for zero tokens", lines[1])
	}
}

// TestSubagentPanelViewTruncation verifies each rendered line is clipped to the
// given terminal width (display columns), never exceeding it.
func TestSubagentPanelViewTruncation(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", strings.Repeat("very long description ", 10), now)
	p.update("a", "", "Searching", 42, now)

	const width = 30
	view := p.view(DefaultTheme(), width, now)
	for _, line := range strings.Split(view, "\n") {
		if w := ui.Width(line); w > width {
			t.Errorf("line width = %d, want <= %d: %q", w, width, line)
		}
	}
}

// TestSubagentPanelViewBlankDescription verifies a row with no description still
// renders (leading with the activity) rather than producing a dangling "·".
func TestSubagentPanelViewBlankDescription(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "", now)
	p.update("a", "", "Thinking", 0, now)
	line := p.view(DefaultTheme(), 100, now)
	if !strings.Contains(line, "Thinking") {
		t.Errorf("view = %q, want activity", line)
	}
	if strings.Contains(line, " · Thinking") {
		t.Errorf("view = %q, blank desc should not leave a leading ' · '", line)
	}
}

// TestSubagentPanelSelection verifies the cursor navigation: a fresh panel has no
// selection, the first ↓ lands on the top row and the first ↑ on the bottom row,
// movement clamps at both ends, and clearSelection resets to no-cursor state.
func TestSubagentPanelSelection(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "task A", now)
	p.add("b", "task B", now)
	p.add("c", "task C", now)

	if p.hasSelection() {
		t.Fatal("fresh panel should have no selection")
	}

	// First ↓ selects the top row; further ↓ advance and clamp at the bottom.
	p.selectDown()
	if !p.hasSelection() || p.selected != 0 {
		t.Fatalf("after first down: hasSelection=%v selected=%d, want true/0", p.hasSelection(), p.selected)
	}
	p.selectDown()
	p.selectDown()
	p.selectDown() // clamp
	if p.selected != 2 {
		t.Errorf("selected after clamp down = %d, want 2", p.selected)
	}

	// ↑ retreats and clamps at the top.
	p.selectUp()
	if p.selected != 1 {
		t.Errorf("selected after up = %d, want 1", p.selected)
	}
	p.selectUp()
	p.selectUp() // clamp
	if p.selected != 0 {
		t.Errorf("selected after clamp up = %d, want 0", p.selected)
	}

	p.clearSelection()
	if p.hasSelection() {
		t.Error("clearSelection should drop the cursor")
	}

	// From no selection, the first ↑ lands on the bottom row.
	p.selectUp()
	if !p.hasSelection() || p.selected != 2 {
		t.Errorf("first up from none: selected=%d, want 2", p.selected)
	}
}

// TestSubagentPanelExpandView verifies that expanding a selected row appends its
// accumulated output below the status line, that the cursor marker is present,
// that lineCount matches the rendered height, and that collapsing removes the
// extra lines.
func TestSubagentPanelExpandView(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "task A", now)
	p.add("b", "task B", now)
	p.appendOutput("b", "hello from B\nsecond line")

	p.selectDown() // selects "a"
	p.selectDown() // selects "b"
	if id := p.expandedID(); id != "" {
		t.Errorf("expandedID before toggle = %q, want empty", id)
	}
	p.toggleExpand()
	if id := p.expandedID(); id != "b" {
		t.Errorf("expandedID after toggle = %q, want b", id)
	}

	const width = 80
	view := p.view(DefaultTheme(), width, now)
	lines := strings.Split(view, "\n")
	if got := p.lineCount(width); got != len(lines) {
		t.Errorf("lineCount = %d, rendered %d lines", got, len(lines))
	}
	// Two status rows + two output lines.
	if len(lines) != 4 {
		t.Fatalf("expanded view has %d lines, want 4: %q", len(lines), view)
	}
	if !strings.Contains(view, "❯") {
		t.Errorf("expanded view missing selection cursor: %q", view)
	}
	if !strings.Contains(view, "hello from B") || !strings.Contains(view, "second line") {
		t.Errorf("expanded view missing output: %q", view)
	}

	// Collapsing reclaims the output lines.
	p.toggleExpand()
	if got := p.lineCount(width); got != 2 {
		t.Errorf("lineCount after collapse = %d, want 2", got)
	}
}

// TestSubagentPanelExpandTruncates verifies the inline output is wrapped and
// tail-capped: every rendered line fits the width and no more than
// maxExpandedLines output lines are shown.
func TestSubagentPanelExpandTruncates(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "task A", now)
	// Many short lines exceed the cap; one very long line must wrap.
	p.appendOutput("a", strings.Repeat("x\n", 40))
	p.appendOutput("a", strings.Repeat("y", 500))
	p.selectDown()
	p.toggleExpand()

	const width = 40
	view := p.view(DefaultTheme(), width, now)
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		if w := ui.Width(line); w > width {
			t.Errorf("line width = %d, want <= %d: %q", w, width, line)
		}
	}
	// 1 status row + at most maxExpandedLines output lines.
	if len(lines) > 1+maxExpandedLines {
		t.Errorf("expanded view has %d lines, want <= %d", len(lines), 1+maxExpandedLines)
	}
}

// TestSubagentPanelRemoveClampsSelection verifies that removing rows keeps the
// selection valid: removing the selected bottom row clamps the cursor to the new
// last row, and removing the final row clears the selection entirely.
func TestSubagentPanelRemoveClampsSelection(t *testing.T) {
	now := time.Now()
	var p subagentPanel
	p.add("a", "task A", now)
	p.add("b", "task B", now)
	p.selectDown()
	p.selectDown() // selects "b" (index 1)

	p.remove("b")
	if !p.hasSelection() || p.selected != 0 {
		t.Errorf("after remove(b): hasSelection=%v selected=%d, want true/0", p.hasSelection(), p.selected)
	}

	p.remove("a")
	if p.hasSelection() {
		t.Error("removing the last row should clear the selection")
	}
	if got := p.lineCount(80); got != 0 {
		t.Errorf("empty panel lineCount = %d, want 0", got)
	}
}

// TestSubagentPanelAppendOutputUnknown verifies deltas for an unknown id are
// dropped (no phantom row, no panic).
func TestSubagentPanelAppendOutputUnknown(t *testing.T) {
	var p subagentPanel
	p.appendOutput("ghost", "data") // must not panic or add a row
	if p.active() != 0 {
		t.Errorf("active after appendOutput to unknown id = %d, want 0", p.active())
	}
}
