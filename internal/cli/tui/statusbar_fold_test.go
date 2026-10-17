package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
)

// Tests for the status bar's fold: which segments stay whole, how the rest
// collapse, and how the pointer and a click move the focus between them.

// foldFixture is a bar with every foldable field populated, so a test can see the
// lead, the slivers, and the one segment that expanded.
func foldFixture() statusBar {
	s := newStatusBar(DefaultTheme(), Options{
		Model:         "claude-opus",
		ProviderName:  "commandcode",
		ThinkingLevel: agentcore.ThinkingHigh,
	}, "/tmp/x")
	s.cwd = "~/work/harness/golder"
	s.SetGit(gitInfoMsg{branch: "master", dirty: 1, ok: true})
	s.SetMode("auto")
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000, tokens: 84000})
	s.SetTask("running: Read")
	return s
}

// foldWidth is a typical terminal: the lead fits whole, the folded fields share
// the rest, and the long values still have to give something up.
const foldWidth = 80

// glyphCol returns the column the first cell carrying r sits at, or -1.
func glyphCol(s statusBar, width int, r rune) int {
	for x, c := range s.row(width) {
		if c.r == r {
			return x
		}
	}
	return -1
}

// TestStatusBarFoldsEveryField is the fold's contract: no field is privileged.
// The provider and the branch fold like the rest — each keeps its glyph and as
// much of its value as its share allows — and none of them disappears.
func TestStatusBarFoldsEveryField(t *testing.T) {
	out := stripANSI(foldFixture().Render(foldWidth))

	for _, want := range []string{
		glyphGit + " …er", glyphMode + " auto", glyphModel + " …opus", glyphThink + " high",
		glyphCwd + " …lder", glyphCtx + " …42%)", glyphTask + " …Read",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("folded field %q should show its window: %q", want, out)
		}
	}
	// The provider is text-only — it has no glyph to keep — so its fold keeps the
	// start and the tail of the name rather than a leading marker.
	if !strings.Contains(out, "…dcode") {
		t.Errorf("the provider should keep a recognisable tail: %q", out)
	}
	for _, absent := range []string{"commandcode", "master", "claude-opus", "~/work/harness/golder", "84,000", "running: Read"} {
		if strings.Contains(out, absent) {
			t.Errorf("folded field should not show %q until focused: %q", absent, out)
		}
	}
}

// TestStatusBarHoverRestoresEveryField: each field — the provider and the branch
// included — comes back at full value when it is the focused one.
func TestStatusBarHoverRestoresEveryField(t *testing.T) {
	s := foldFixture()
	for _, tc := range []struct{ glyph, want string }{
		{"", "commandcode"},
		{glyphGit, glyphGit + " master"},
		{glyphMode, "auto"},
		{glyphModel, "claude-opus"},
		{glyphProto, "…"}, // the fixture sets no protocol; covered by its own test
		{glyphThink, "high"},
		{glyphCwd, "~/work/harness/golder"},
		{glyphCtx, "84,000"},
		{glyphTask, "running: Read"},
	} {
		if tc.glyph == glyphProto {
			continue
		}
		focused := s
		if tc.glyph == "" {
			focused.hover = focusIndex(0)
		} else {
			focused = focusSegment(t, s, tc.glyph)
		}
		if out := stripANSI(focused.Render(foldWidth)); !strings.Contains(out, tc.want) {
			t.Errorf("focused %q should show %q: %q", tc.glyph, tc.want, out)
		}
	}
}

// TestStatusBarHoverExpandsOnlyTheHoveredSegment: the focused field takes its
// whole width back, everything else stays folded, and the row is still exactly the
// terminal's width.
func TestStatusBarHoverExpandsOnlyTheHoveredSegment(t *testing.T) {
	s := focusSegment(t, foldFixture(), glyphModel)
	out := stripANSI(s.Render(foldWidth))

	if !strings.Contains(out, "claude-opus") {
		t.Errorf("the hovered field should expand: %q", out)
	}
	if !strings.Contains(out, glyphTask) || strings.Contains(out, "running: Read") {
		t.Errorf("the other fields should stay folded: %q", out)
	}
	if w := ui.Width(out); w != foldWidth {
		t.Errorf("row width = %d, want exactly %d: %q", w, foldWidth, out)
	}
}

// TestStatusBarPinExpandsWithoutHover: a click's pin keeps a field open, and a
// hover takes precedence over it — one segment at a time, never two.
func TestStatusBarPinExpandsWithoutHover(t *testing.T) {
	s := foldFixture()
	s.pin = focusIndex(len(s.segments()) - 1) // the task, the last field
	if out := stripANSI(s.Render(foldWidth)); !strings.Contains(out, "running: Read") {
		t.Errorf("a pinned field should be expanded without any hover: %q", out)
	}

	s.hover = focusIndex(3) // the model
	out := stripANSI(s.Render(foldWidth))
	if !strings.Contains(out, "claude-opus") {
		t.Errorf("the hovered field should expand: %q", out)
	}
	if strings.Contains(out, "running: Read") {
		t.Errorf("a hover replaces the pin's expansion, so only one field is open: %q", out)
	}
}

// TestStatusBarSegmentAtFindsEveryField pins the hit test against what is
// painted: the column a field's glyph sits at belongs to that field, right down to
// the one-character slivers, and the bar's background belongs to nobody.
func TestStatusBarSegmentAtFindsEveryField(t *testing.T) {
	s := foldFixture()
	segs := s.segments()
	for i, seg := range segs {
		glyph, _, _ := strings.Cut(seg.text, " ")
		r := []rune(glyph)[0]
		if r == ' ' {
			continue // the provider has no glyph; its first cell is its text
		}
		col := glyphCol(s, foldWidth, r)
		if col < 0 {
			t.Fatalf("segment %q is not painted at all", seg.text)
		}
		if got := s.segmentAt(col, foldWidth); got != focusIndex(i) {
			t.Errorf("segmentAt(%d) = %d, want %d (%q)", col, got.index(), i, seg.text)
		}
	}
	if got := s.segmentAt(foldWidth-1, foldWidth); got != 0 {
		t.Errorf("the bar's background should belong to no segment, got %d", got.index())
	}
}

// TestStatusBarFoldedRowIsExactlyTheRequestedWidth: the cell row cannot overflow
// or fall short, whatever the terminal width — the old renderer needed a
// truncation fallback for that, and the canvas makes it structural.
func TestStatusBarFoldedRowIsExactlyTheRequestedWidth(t *testing.T) {
	for _, plain := range []bool{false, true} {
		s := foldFixture()
		s.plain = plain
		for width := 1; width <= 80; width++ {
			got := s.Render(width)
			if w := ui.Width(got); w != width {
				t.Fatalf("plain=%v width %d: row is %d columns: %q", plain, width, w, stripANSI(got))
			}
		}
	}
}

// TestStatusBarFoldedFieldsUseArrows: a folded field is separated from its
// neighbour by the bar's own powerline arrow, not by the hard vertical edge of two
// adjacent blocks. The arrow is what makes the row read as one ribbon.
func TestStatusBarFoldedFieldsUseArrows(t *testing.T) {
	s := foldFixture()
	segs := s.foldLayout(foldWidth)
	out := stripANSI(s.Render(foldWidth))
	if got, want := strings.Count(out, sepArrow), len(segs); got != want {
		t.Errorf("arrows = %d, want %d (one between each pair and one closing): %q", got, want, out)
	}
	if strings.Contains(out, "│") {
		t.Errorf("folded fields should not be separated by a vertical rule: %q", out)
	}
}

// TestStatusBarFoldedFieldsUseTheWholeRow: the fold is not a fixed three-column
// stub per field. The layout spends every column the lead and the focused field do
// not need, so the folded values grow to fill the terminal instead of leaving a
// strip of empty bar.
func TestStatusBarFoldedFieldsUseTheWholeRow(t *testing.T) {
	s := foldFixture()
	segs := s.foldLayout(foldWidth)
	used := 0
	for _, seg := range segs {
		text := s.segText(seg)
		if seg.folded {
			if got := ui.Width(text); got != seg.win {
				t.Errorf("folded %q painted %d columns, its window is %d", text, got, seg.win)
			}
			if seg.win < foldMinWidth(seg.text) {
				t.Errorf("folded %q window %d is below its minimum %d", seg.text, seg.win, foldMinWidth(seg.text))
			}
		}
		used += ui.Width(text)
		if !s.plain {
			used += 2
		}
	}
	if !s.plain {
		used += len(segs) // between + closing arrows
	} else {
		used += (len(segs) - 1) * ui.Width(sepPlain)
	}
	if used != foldWidth {
		t.Errorf("the row uses %d columns, want %d: %q", used, foldWidth, stripANSI(s.Render(foldWidth)))
	}
}

// TestStatusBarHoverShowsTheWholePath: the path is the field a person most often
// needs in full, and a hover gives it its natural width before the other folded
// fields take their share.
func TestStatusBarHoverShowsTheWholePath(t *testing.T) {
	s := foldFixture()
	s = focusSegment(t, s, glyphCwd)
	const width = 120
	out := stripANSI(s.Render(width))
	if !strings.Contains(out, "~/work/harness/golder") {
		t.Errorf("the hovered path should be shown whole: %q", out)
	}
	if w := ui.Width(out); w != width {
		t.Errorf("row width = %d, want %d: %q", w, width, out)
	}
}

// TestStatusBarSegmentAtAgreesWithTheRow: the hit test walks the layout instead of
// reading the painted row (a hover must not cost a canvas per pointer move), so the
// two walks have to be pinned against each other — every column, both forms, with
// and without a focused field. A drift here would put the pointer on a field other
// than the one under it, which is exactly the kind of bug a person blames on the
// mouse.
func TestStatusBarSegmentAtAgreesWithTheRow(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, focus := range []segFocus{0, focusIndex(3), focusIndex(5)} {
			s := foldFixture()
			s.plain = plain
			s.hover = focus
			for _, width := range []int{24, 60, 120} {
				row := s.row(width)
				for x := 0; x < width; x++ {
					want := segFocus(row[x].seg)
					if got := s.segmentAt(x, width); got != want {
						t.Fatalf("plain=%v focus=%d width=%d column %d: segmentAt = %d, painted = %d",
							plain, focus.index(), width, x, got.index(), want.index())
					}
				}
			}
		}
	}
}

// TestStatusBarFoldHandlesWideRunes: a CJK value occupies two columns, and both of
// them stay addressable — the pointer on either half hits the same field, and the
// row keeps its exact width.
func TestStatusBarFoldHandlesWideRunes(t *testing.T) {
	s := foldFixture()
	s.SetTask("运行测试")
	s = focusSegment(t, s, glyphTask)

	const width = 120
	out := stripANSI(s.Render(width))
	if w := ui.Width(out); w != width {
		t.Fatalf("row width = %d, want %d: %q", w, width, out)
	}
	task := focusIndex(len(s.segments()) - 1)
	if !strings.Contains(out, "运行测试") {
		t.Fatalf("the focused task should show its value: %q", out)
	}
	row := s.row(width)
	for x, c := range row {
		if c.r == '运' {
			if got := s.segmentAt(x, width); got != task {
				t.Errorf("segmentAt(%d) = %d, want the task", x, got.index())
			}
			// The continuation cell of the wide rune belongs to the same field.
			if got := s.segmentAt(x+1, width); got != task {
				t.Errorf("segmentAt(%d) (the rune's second column) = %d, want the task", x+1, got.index())
			}
			return
		}
	}
	t.Fatal("the wide rune was not painted")
}

// TestModelStatusBarHoverFollowsThePointer drives the interaction through Update:
// motion on the bar's row takes the hover, motion anywhere else drops it.
func TestModelStatusBarHoverFollowsThePointer(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"}), tea.WindowSizeMsg{Width: 120, Height: 30})
	m.statusBar.SetGit(gitInfoMsg{branch: "master", ok: true})
	m.statusBar.SetTask("running: Read")

	barRow := m.height - 1
	col := glyphCol(m.statusBar, m.width, '⏵')
	if col < 0 {
		t.Fatal("the task sliver is not painted")
	}

	m = apply(t, m, tea.MouseMotionMsg{X: col, Y: barRow})
	if m.statusBar.hover != focusIndex(len(m.statusBar.segments())-1) {
		t.Fatalf("hover = %d, want the task segment", m.statusBar.hover.index())
	}

	m = apply(t, m, tea.MouseMotionMsg{X: 4, Y: barRow - 3})
	if m.statusBar.hover != 0 {
		t.Errorf("motion off the bar should drop the hover, got %d", m.statusBar.hover.index())
	}
}

// TestModelStatusBarClickPinsAndUnpins: the click is the fold's fallback for a
// terminal that reports no motion events. It pins the field under it, a second
// click on the same field releases it, a click on the bar's background clears it,
// and none of them start a text selection on the bar.
func TestModelStatusBarClickPinsAndUnpins(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"}), tea.WindowSizeMsg{Width: 120, Height: 30})
	m.statusBar.SetGit(gitInfoMsg{branch: "master", ok: true})
	m.statusBar.SetTask("running: Read")

	barRow := m.height - 1
	col := glyphCol(m.statusBar, m.width, '⏵')
	if col < 0 {
		t.Fatal("the task sliver is not painted")
	}
	task := m.statusSegmentAt(col, barRow)

	m = apply(t, m, tea.MouseClickMsg{X: col, Y: barRow, Button: tea.MouseLeft})
	if m.statusBar.pin != task {
		t.Fatalf("pin = %d, want the clicked segment %d", m.statusBar.pin.index(), task.index())
	}
	if m.sel.active {
		t.Error("a press on the status bar should not start a text selection")
	}

	m = apply(t, m, tea.MouseClickMsg{X: col, Y: barRow, Button: tea.MouseLeft})
	if m.statusBar.pin != 0 {
		t.Errorf("a second press on the same field should release the pin, got %d", m.statusBar.pin.index())
	}

	m = apply(t, m, tea.MouseClickMsg{X: col, Y: barRow, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseClickMsg{X: m.width - 1, Y: barRow, Button: tea.MouseLeft})
	if m.statusBar.pin != 0 {
		t.Errorf("a press on the bar's background should release the pin, got %d", m.statusBar.pin.index())
	}
}

// TestModelStatusBarHoverLeavesADragAlone: a text selection dragged across the bar
// keeps extending — the hover must not steal motion that belongs to the drag, or
// selecting text past the last transcript line would snap the bar's focus around.
func TestModelStatusBarHoverLeavesADragAlone(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"}), tea.WindowSizeMsg{Width: 120, Height: 30})
	m.statusBar.SetTask("running: Read")
	m.transcript.addSystem("first\nsecond\nthird")
	m.relayout()

	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: 0, Button: tea.MouseLeft})
	if !m.selDragging {
		t.Fatal("a press in the transcript should start a drag")
	}
	barRow := m.height - 1
	m = apply(t, m, tea.MouseMotionMsg{X: 4, Y: barRow, Button: tea.MouseLeft})

	if m.statusBar.hover != 0 {
		t.Errorf("a drag over the bar should not take the hover, got %d", m.statusBar.hover.index())
	}
	// The drag still belongs to the selection: a content-anchored drag that
	// leaves the viewport clamps onto the last content line (that is the existing
	// contract — see dragToSel) instead of becoming a screen-row selection, and
	// it is still live.
	if !m.sel.active {
		t.Error("a drag onto the bar should keep extending the selection")
	}
	if n := len(m.transcript.contentLines()); n > 0 && m.sel.cursor.y != n-1 {
		t.Errorf("cursor line = %d, want it clamped onto the last content line %d", m.sel.cursor.y, n-1)
	}
}

// TestModelMouseModeIsAllMotion pins the terminal mode the fold depends on: motion
// without a button held is xterm 1003, and without it a hover can never arrive.
func TestModelMouseModeIsAllMotion(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := m.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Errorf("mouse mode = %v, want MouseModeAllMotion (1003)", got)
	}
}
