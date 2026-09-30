package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// This file implements mouse text selection over the rendered shell. Because the
// model paints the whole screen as one string (transcript + menu + input +
// status), a selection is expressed in screen cells (0-based, top-left origin)
// and spans any region uniformly — dragging across transcript output or the
// input line both work. The left mouse button starts a selection at the press
// cell and extends it on drag; the selection persists after release so Ctrl+C
// can copy it. A plain click (no drag) leaves an empty selection, which clears
// any prior highlight and lets Ctrl+C fall back to its interrupt/quit role.

// maxCol is a sentinel column that reaches past the end of any rendered row, so
// a multi-row selection's interior rows select through to their line end.
const maxCol = 1 << 30

// point is a screen cell: x is the column, y the row, both 0-based from the
// top-left of the rendered shell.
type point struct{ x, y int }

// selection is an in-progress or completed text selection. anchor is where the
// drag began and cursor is the latest drag point; active is set between the
// initial press and the next fresh press. It is a plain value so the Model can
// hold and copy it cheaply.
//
// Endpoints are transcript content-line indices (point.y into
// transcript.contentLines, point.x a display column) whenever the press landed
// inside the transcript region, so wheel/page scrolling preserves the highlight
// instead of clearing it. below is true when the press landed under the
// transcript (menu/input/status rows): those endpoints are plain screen rows
// with legacy single-page behavior (cleared on scroll).
type selection struct {
	active bool
	anchor point
	cursor point
	below  bool
}

// empty reports whether the selection covers no cells — either inactive or a
// bare click where the cursor never moved off the anchor. Ctrl+C treats an empty
// selection as "nothing to copy" and keeps its interrupt/quit behavior.
func (s selection) empty() bool {
	return !s.active || s.anchor == s.cursor
}

// ordered returns the selection endpoints in reading order (top-to-bottom, and
// left-to-right within a row), so callers can walk rows start.y..end.y without
// re-checking which of anchor/cursor came first.
func (s selection) ordered() (start, end point) {
	a, c := s.anchor, s.cursor
	if a.y > c.y || (a.y == c.y && a.x > c.x) {
		return c, a
	}
	return a, c
}

// rowRange computes the selected column span [c0, c1) on screen row y for a
// selection running start..end. Interior rows of a multi-row selection run from
// column 0 through the line end (maxCol); the first row starts at start.x and
// the last ends at end.x. ok is false when y falls outside the selection.
func rowRange(start, end point, y int) (c0, c1 int, ok bool) {
	if y < start.y || y > end.y {
		return 0, 0, false
	}
	c0, c1 = 0, maxCol
	if y == start.y {
		c0 = start.x
	}
	if y == end.y {
		c1 = end.x
	}
	if c0 < 0 {
		c0 = 0
	}
	if c1 < c0 {
		c1 = c0
	}
	return c0, c1, true
}

// selectRow walks one rendered row (ANSI stripped to plain cells) and returns
// both the row with the selected span visually highlighted and the selected
// text itself. Columns are measured in display cells (ui.Width) so double-width
// runes are never split. The highlight is applied over plain text — the row's
// original coloring is dropped on the intersected row while a selection is live,
// which keeps the overlay ANSI-safe without parsing the embedded escapes.
func selectRow(row string, c0, c1 int, hi lipgloss.Style) (highlighted, text string) {
	plain := ansi.Strip(row)

	var out, sel, run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			out.WriteString(hi.Render(run.String()))
			run.Reset()
		}
	}

	col := 0
	for _, r := range plain {
		w := ui.Width(string(r))
		if col >= c0 && col < c1 {
			run.WriteRune(r)
			sel.WriteRune(r)
		} else {
			flush()
			out.WriteRune(r)
		}
		col += w
	}
	flush()
	return out.String(), sel.String()
}
