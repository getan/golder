package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TestRowRange checks the per-row column span for single- and multi-row
// selections, and that rows outside the range report ok=false.
func TestRowRange(t *testing.T) {
	start, end := point{3, 1}, point{7, 3}

	if _, _, ok := rowRange(start, end, 0); ok {
		t.Error("row above the selection should not be selected")
	}
	if c0, c1, ok := rowRange(start, end, 1); !ok || c0 != 3 || c1 != maxCol {
		t.Errorf("first row = (%d,%d,%v), want (3,maxCol,true)", c0, c1, ok)
	}
	if c0, c1, ok := rowRange(start, end, 2); !ok || c0 != 0 || c1 != maxCol {
		t.Errorf("interior row = (%d,%d,%v), want (0,maxCol,true)", c0, c1, ok)
	}
	if c0, c1, ok := rowRange(start, end, 3); !ok || c0 != 0 || c1 != 7 {
		t.Errorf("last row = (%d,%d,%v), want (0,7,true)", c0, c1, ok)
	}
	if _, _, ok := rowRange(start, end, 4); ok {
		t.Error("row below the selection should not be selected")
	}

	// A single-row selection uses [start.x, end.x).
	if c0, c1, ok := rowRange(point{2, 5}, point{9, 5}, 5); !ok || c0 != 2 || c1 != 9 {
		t.Errorf("single row = (%d,%d,%v), want (2,9,true)", c0, c1, ok)
	}
}

// TestSelectRowExtracts verifies the selected text is the column-clipped slice
// of the row, measured in display cells so CJK is never split, and that ANSI in
// the source row is stripped before slicing.
func TestSelectRowExtracts(t *testing.T) {
	hi := lipgloss.NewStyle().Reverse(true)

	if _, text := selectRow("hello world", 0, 5, hi); text != "hello" {
		t.Errorf("selected %q, want %q", text, "hello")
	}
	if _, text := selectRow("hello world", 6, maxCol, hi); text != "world" {
		t.Errorf("selected %q, want %q", text, "world")
	}

	// ANSI coloring in the source is stripped before the selection is measured.
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("hello")
	if _, text := selectRow(styled, 0, maxCol, hi); text != "hello" {
		t.Errorf("selected %q from styled row, want %q", text, "hello")
	}

	// CJK counts as two columns: selecting the first two cells yields one rune.
	if _, text := selectRow("你好ab", 0, 2, hi); text != "你" {
		t.Errorf("selected %q, want %q (double-width clipped on a cell boundary)", text, "你")
	}
}

// TestSelAutoscrollDragAcrossPages drives a press + drag pinned at the
// transcript edge and asserts the viewport scrolls under the content-anchored
// cursor (endpoints are content lines, so they stay put while the offset moves),
// that leaving the edge stops the loop, and that stale ticks never move a
// released selection.
func TestSelAutoscrollDragAcrossPages(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	for i := 0; i < 30; i++ {
		m.transcript.addUser("line")
	}
	if !m.transcript.overflowing() {
		t.Fatal("expected the transcript to overflow")
	}
	vh := m.transcript.viewportHeight()
	if vh < 5 {
		t.Fatalf("viewportHeight = %d, want >= 5 for edge/middle rows", vh)
	}
	top, bottom, mid := 0, vh-1, vh/2

	// Drag pinned at the top edge scrolls up; the content-anchored cursor stays
	// on its line while the offset moves under it.
	m = apply(t, m, tea.MouseClickMsg{X: 2, Y: mid, Button: tea.MouseLeft})
	off0 := m.transcript.vp.YOffset()
	if m.sel.anchor != (point{2, off0 + mid}) {
		t.Fatalf("anchor = %+v, want content line {2 %d}", m.sel.anchor, off0+mid)
	}
	next, cmd := m.Update(tea.MouseMotionMsg{X: 2, Y: top, Button: tea.MouseLeft})
	m = next.(Model)
	if m.selScrollDir != -1 {
		t.Fatalf("selScrollDir = %d, want -1 at the top edge", m.selScrollDir)
	}
	if cmd == nil {
		t.Fatal("pinning the edge should start the autoscroll tick")
	}
	if before := m.transcript.vp.YOffset(); before <= 0 {
		t.Fatal("expected room above the bottom-pinned viewport")
	}
	wantCursor := point{2, off0 + top}
	if m.sel.cursor != wantCursor {
		t.Fatalf("cursor = %+v, want content line %+v", m.sel.cursor, wantCursor)
	}
	m = apply(t, m, selScrollTickMsg{gen: m.selScrollGen})
	if got := m.transcript.vp.YOffset(); got >= off0 {
		t.Errorf("YOffset = %d after up-tick, want < %d", got, off0)
	}
	if m.sel.cursor != wantCursor {
		t.Errorf("cursor moved to %+v across the tick, want kept %+v", m.sel.cursor, wantCursor)
	}
	if !m.sel.active {
		t.Error("selection should stay active across autoscroll ticks")
	}

	// Moving off the edge stops the loop; the in-flight tick is then dropped.
	m = apply(t, m, tea.MouseMotionMsg{X: 2, Y: mid, Button: tea.MouseLeft})
	if m.selScrollDir != 0 {
		t.Errorf("selScrollDir = %d after leaving the edge, want 0", m.selScrollDir)
	}
	parked := m.transcript.vp.YOffset()
	m = apply(t, m, selScrollTickMsg{gen: m.selScrollGen})
	if got := m.transcript.vp.YOffset(); got != parked {
		t.Errorf("YOffset moved to %d after the loop stopped, want %d", got, parked)
	}

	// Drag pinned at the bottom edge scrolls down (room below after scrolling up).
	m = apply(t, m, tea.MouseMotionMsg{X: 2, Y: bottom, Button: tea.MouseLeft})
	if m.selScrollDir != 1 {
		t.Fatalf("selScrollDir = %d, want +1 at the bottom edge", m.selScrollDir)
	}
	m = apply(t, m, selScrollTickMsg{gen: m.selScrollGen})
	if got := m.transcript.vp.YOffset(); got <= parked {
		t.Errorf("YOffset = %d after down-tick, want > %d", got, parked)
	}

	// Release stops the loop and a stale generation tick is dropped.
	m = apply(t, m, tea.MouseReleaseMsg{X: 2, Y: bottom, Button: tea.MouseLeft})
	if m.selScrollDir != 0 {
		t.Errorf("selScrollDir = %d after release, want 0", m.selScrollDir)
	}
	parked = m.transcript.vp.YOffset()
	m = apply(t, m, selScrollTickMsg{gen: m.selScrollGen - 1})
	if got := m.transcript.vp.YOffset(); got != parked {
		t.Errorf("stale tick moved YOffset to %d, want %d", got, parked)
	}
	if m.sel.empty() {
		t.Error("released selection should persist for Ctrl+C")
	}
}

// TestShiftClickExtendsSelection verifies shift+left-click keeps the previous
// press as the anchor and jumps only the cursor there, selecting everything in
// between across pages in either direction; without a prior selection it starts
// a fresh one like a plain press.
func TestShiftClickExtendsSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	for i := 0; i < 30; i++ {
		m.transcript.addUser("line")
	}
	vh := m.transcript.viewportHeight()
	top, bottom := 0, vh-1

	// Plain press sets the anchor (a content line); shift+click far below extends
	// to it. Endpoints are content-anchored: screen row + viewport offset.
	off := m.transcript.vp.YOffset()
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: top + 1, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 1, Y: top + 1, Button: tea.MouseLeft})
	anchor := m.sel.anchor
	if anchor != (point{1, off + top + 1}) {
		t.Fatalf("anchor = %+v, want content line {1 %d}", anchor, off+top+1)
	}
	m = apply(t, m, tea.MouseClickMsg{X: 5, Y: bottom, Button: tea.MouseLeft, Mod: tea.ModShift})
	if m.sel.anchor != anchor {
		t.Errorf("shift+click moved anchor to %+v, want kept %+v", m.sel.anchor, anchor)
	}
	if want := (point{5, off + bottom}); m.sel.cursor != want {
		t.Errorf("shift+click cursor = %+v, want %+v", m.sel.cursor, want)
	}
	if m.sel.empty() {
		t.Error("extended selection should not be empty")
	}
	if text := m.selectedText(); text == "" {
		t.Error("extended selection should copy text across rows")
	}

	// Shift+click back upward re-aims the cursor while the anchor stays put.
	m = apply(t, m, tea.MouseClickMsg{X: 0, Y: top, Button: tea.MouseLeft, Mod: tea.ModShift})
	if m.sel.anchor != anchor {
		t.Errorf("second shift+click moved anchor to %+v, want kept %+v", m.sel.anchor, anchor)
	}
	if want := (point{0, off + top}); m.sel.cursor != want {
		t.Errorf("second shift+click cursor = %+v, want %+v", m.sel.cursor, want)
	}

	// With no prior selection, shift+click starts fresh like a plain press.
	fresh := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	fresh = apply(t, fresh, tea.MouseClickMsg{X: 3, Y: 2, Button: tea.MouseLeft, Mod: tea.ModShift})
	if fresh.sel.anchor != (point{3, 2}) || fresh.sel.cursor != (point{3, 2}) {
		t.Errorf("shift+click without prior selection = %+v, want fresh press at {3 2}", fresh.sel)
	}
}

// TestHoverDoesNotClobberReleasedSelection is the shift+click follow-up guard:
// motion after release (button up) must not move the cursor, otherwise a
// finished selection collapses under hover and shift+click has nothing to
// extend. Motion while the button is held still extends the drag.
func TestHoverDoesNotClobberReleasedSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	for i := 0; i < 10; i++ {
		m.transcript.addUser("line")
	}

	// Drag with the button held extends the cursor (content-anchored).
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: 2, Button: tea.MouseLeft})
	off := m.transcript.vp.YOffset()
	m = apply(t, m, tea.MouseMotionMsg{X: 5, Y: 4, Button: tea.MouseLeft})
	if want := (point{5, off + 4}); m.sel.cursor != want {
		t.Fatalf("drag cursor = %+v, want %+v", m.sel.cursor, want)
	}

	// Release, then hover elsewhere: the cursor must stay put.
	m = apply(t, m, tea.MouseReleaseMsg{X: 5, Y: 4, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseMotionMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseMotionMsg{X: 9, Y: 9, Button: tea.MouseLeft})
	if want := (point{5, off + 4}); m.sel.cursor != want {
		t.Errorf("hover moved cursor to %+v, want kept %+v", m.sel.cursor, want)
	}
	if m.sel.empty() {
		t.Error("released selection should persist for shift+click / Ctrl+C")
	}

	// The preserved selection is still extendable by shift+click.
	m = apply(t, m, tea.MouseClickMsg{X: 2, Y: 6, Button: tea.MouseLeft, Mod: tea.ModShift})
	if m.sel.anchor != (point{1, off + 2}) {
		t.Errorf("shift+click moved anchor to %+v, want {1 %d}", m.sel.anchor, off+2)
	}
	if m.sel.cursor != (point{2, off + 6}) {
		t.Errorf("shift+click cursor = %+v, want {2 %d}", m.sel.cursor, off+6)
	}
}

// TestWheelScrollPreservesSelection verifies a two-finger scroll with a live
// content selection neither clears nor grows it: endpoints stay on their
// content lines while the viewport moves, so the highlight tracks the text.
// Cross-page extension stays the edge-drag's job. A below-transcript selection
// keeps the legacy clear-on-scroll.
func TestWheelScrollPreservesSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	for i := 0; i < 30; i++ {
		m.transcript.addUser("line")
	}
	vh := m.transcript.viewportHeight()

	// Select a small range and release, then scroll both ways: endpoints kept.
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: vh - 2, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseMotionMsg{X: 4, Y: vh - 2, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 4, Y: vh - 2, Button: tea.MouseLeft})
	anchor, cursor := m.sel.anchor, m.sel.cursor
	if m.sel.empty() {
		t.Fatal("expected a live selection after drag")
	}
	before := m.transcript.vp.YOffset()
	m = apply(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if after := m.transcript.vp.YOffset(); after >= before {
		t.Fatalf("wheel up: YOffset = %d, want < %d", after, before)
	}
	if m.sel.empty() {
		t.Fatal("wheel must not clear a content-anchored selection")
	}
	if m.sel.anchor != anchor || m.sel.cursor != cursor {
		t.Errorf("wheel moved endpoints to %+v/%+v, want kept %+v/%+v",
			m.sel.anchor, m.sel.cursor, anchor, cursor)
	}
	m = apply(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.sel.anchor != anchor || m.sel.cursor != cursor {
		t.Errorf("wheel down moved endpoints to %+v/%+v, want kept %+v/%+v",
			m.sel.anchor, m.sel.cursor, anchor, cursor)
	}

	// A below-transcript press (input area) keeps legacy clear-on-scroll.
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: vh + 2, Button: tea.MouseLeft})
	if !m.sel.below {
		t.Fatalf("press below the transcript should set fallback, got %+v", m.sel)
	}
	m = apply(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if !m.sel.empty() {
		t.Errorf("wheel should clear a below-transcript selection, got %+v", m.sel)
	}
}

// TestResizeClearsSelection verifies a terminal resize drops a live selection:
// re-wrapping re-indexes every content line, so endpoints would point at the
// wrong text.
func TestResizeClearsSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m.transcript.addUser("line")
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseMotionMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 4, Y: 0, Button: tea.MouseLeft})
	if m.sel.empty() {
		t.Fatal("expected a live selection after drag")
	}
	m = apply(t, m, tea.WindowSizeMsg{Width: 50, Height: 14})
	if !m.sel.empty() {
		t.Errorf("resize should clear the selection, got %+v", m.sel)
	}
}

// TestDragBelowTranscriptPinsToEnd verifies the generous bottom zone: a
// content-anchored selection dragged anywhere below the transcript (input,
// status, window bottom) pins to the last content line and arms scroll-down,
// while a selection born below the transcript never scrolls it.
func TestDragBelowTranscriptPinsToEnd(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	for i := 0; i < 30; i++ {
		m.transcript.addUser("line")
	}
	vh := m.transcript.viewportHeight()
	n := len(m.transcript.contentLines())
	if n == 0 {
		t.Fatal("expected cached content lines")
	}

	// Drag from mid-transcript down past the window bottom row.
	m = apply(t, m, tea.MouseClickMsg{X: 1, Y: vh / 2, Button: tea.MouseLeft})
	anchor := m.sel.anchor
	m = apply(t, m, tea.MouseMotionMsg{X: 3, Y: 99, Button: tea.MouseLeft})
	if want := (point{maxCol, n - 1}); m.sel.cursor != want {
		t.Errorf("below-window cursor = %+v, want pinned %+v", m.sel.cursor, want)
	}
	if m.sel.below {
		t.Error("content selection dragged below must not flip to fallback")
	}
	if m.selScrollDir != 1 {
		t.Errorf("selScrollDir = %d, want +1 for the below-transcript zone", m.selScrollDir)
	}
	if m.sel.anchor != anchor {
		t.Errorf("drag moved anchor to %+v, want kept %+v", m.sel.anchor, anchor)
	}

	// Release at the window bottom keeps select-to-end for Ctrl+C.
	m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 99, Button: tea.MouseLeft})
	if want := (point{maxCol, n - 1}); m.sel.cursor != want {
		t.Errorf("released cursor = %+v, want %+v", m.sel.cursor, want)
	}
	if text := m.selectedText(); !strings.Contains(text, "line") {
		t.Errorf("select-to-end should copy transcript text, got %q", text)
	}
}
