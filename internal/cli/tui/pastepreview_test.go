package tui

// Tests for the paste placeholder's shape and the Ctrl+O preview
// (pastepreview.go): which pastes collapse, what the token says, and that a
// preview opens, pages, collapses and never disturbs the draft it is read from.

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// pasteLines builds a body of n numbered lines.
func pasteLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	return strings.TrimRight(b.String(), "\n")
}

// pasteInto drives one bracketed paste into a sized model.
func pasteInto(t *testing.T, content string) Model {
	t.Helper()
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	return apply(t, m, tea.PasteMsg{Content: content})
}

// ctrlO presses Ctrl+O.
func ctrlO(t *testing.T, m Model) Model {
	t.Helper()
	return apply(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
}

// TestPasteCollapseRules pins which payloads collapse: any multi-line paste
// (the long-standing rule, unchanged) and a single line past the cutoff (the
// addition — a one-line blob used to sail straight into the composer).
func TestPasteCollapseRules(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		collapsed bool
	}{
		{"two short lines collapse", "a\nb", true},
		{"one line with its terminating newline stays inline", "a\n", false},
		{"short single line stays inline", strings.Repeat("x", 20), false},
		{"single line at the cutoff stays inline", strings.Repeat("x", pasteCollapseChars), false},
		{"single line past the cutoff collapses", strings.Repeat("x", pasteCollapseChars+1), true},
		{"a trailing blank line is a line", "a\n\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := pasteInto(t, c.content)
			value := m.input.Value()
			if got := strings.Contains(value, "[Pasted text #1"); got != c.collapsed {
				t.Fatalf("collapsed = %v, want %v (composer %q)", got, c.collapsed, value)
			}
			if c.collapsed {
				if got := m.expandPastes(value); got != c.content {
					t.Errorf("expandPastes = %q, want the original body", got)
				}
			} else if value != c.content {
				t.Errorf("inline paste = %q, want the payload verbatim", value)
			}
		})
	}
}

// TestPastePlaceholderShape pins the token: the id, the line count for a
// multi-line body, and a size either way.
func TestPastePlaceholderShape(t *testing.T) {
	if got, want := pastePlaceholder(1, "line1\nline2\nline3"), "[Pasted text #1 · 3 lines · 17 B]"; got != want {
		t.Errorf("multi-line token = %q, want %q", got, want)
	}
	if got, want := pastePlaceholder(2, strings.Repeat("x", 1500)), "[Pasted text #2 · 1.5 KB]"; got != want {
		t.Errorf("single-line token = %q, want %q", got, want)
	}
	if got, want := humanBytes(2*1024*1024), "2.0 MB"; got != want {
		t.Errorf("humanBytes(2 MiB) = %q, want %q", got, want)
	}
}

// TestPastePreviewTogglePageClose drives the whole key story: Ctrl+O opens on
// the newest paste, PgDn/PgUp page the body, Ctrl+O collapses, Esc closes —
// and Esc must close the preview rather than reach the interrupt/quit arm.
func TestPastePreviewTogglePageClose(t *testing.T) {
	body := pasteLines(20)
	m := pasteInto(t, body)

	m = ctrlO(t, m)
	if m.pastePreview == nil || m.pastePreview.id != 1 {
		t.Fatalf("ctrl+o did not open the paste, preview = %+v", m.pastePreview)
	}
	rows := pastePreviewMaxRows(m.height)
	block := m.pastePreviewLines(m.width)
	if len(block) != rows+1 { // body window + footer
		t.Fatalf("block has %d rows, want %d body rows plus the footer", len(block), rows)
	}
	if got := strings.Join(block, "\n"); !strings.Contains(got, fmt.Sprintf("lines 1–%d of 20", rows)) {
		t.Errorf("opened at the wrong position:\n%s", got)
	}
	if got := strings.Join(block, "\n"); !strings.Contains(got, "line 01") {
		t.Errorf("first body line missing:\n%s", got)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got := strings.Join(m.pastePreviewLines(m.width), "\n"); !strings.Contains(got, fmt.Sprintf("lines %d–%d of 20", rows+1, rows*2)) {
		t.Errorf("PgDn did not page forward:\n%s", got)
	}
	// Paging past the end clamps to the last window instead of running away.
	for i := 0; i < 5; i++ {
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if got := strings.Join(m.pastePreviewLines(m.width), "\n"); !strings.Contains(got, "lines 13–20 of 20") {
		t.Errorf("paging did not clamp at the end:\n%s", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if got := strings.Join(m.pastePreviewLines(m.width), "\n"); !strings.Contains(got, "lines 5–12 of 20") {
		t.Errorf("PgUp did not page back:\n%s", got)
	}

	m = ctrlO(t, m)
	if m.pastePreview != nil {
		t.Errorf("a second ctrl+o should collapse the preview, got %+v", m.pastePreview)
	}

	m = ctrlO(t, m)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.pastePreview != nil {
		t.Errorf("esc should close the preview, got %+v", m.pastePreview)
	}
	if m.quitting {
		t.Error("esc on an open preview must close it, not arm the quit")
	}
}

// TestPasteHintTeachesTheKey pins the discoverability contract: a draft holding
// a collapsed paste carries a visible ctrl+o hint, so the key is learnable
// without reading docs. The hint steps aside for the open preview's footer
// (which already names the keys), disappears with the token, and stays out of
// the mid-run composer, where the key does nothing.
func TestPasteHintTeachesTheKey(t *testing.T) {
	// No paste, no hint.
	empty := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := empty.pasteHintLine(empty.width); got != "" {
		t.Errorf("an empty draft must not carry a hint, got %q", got)
	}

	m := pasteInto(t, pasteLines(5))
	hint := stripTCardANSI(m.pasteHintLine(m.width))
	if !strings.Contains(hint, "ctrl+o") {
		t.Fatalf("hint = %q, want the key named", hint)
	}
	// The hint must be visible in the frame, inside the composer.
	if view := stripTCardANSI(m.View().Content); !strings.Contains(view, "ctrl+o previews it here") {
		t.Errorf("the hint is not in the rendered frame:\n%s", view)
	}

	// The open preview's footer takes over.
	m = ctrlO(t, m)
	if m.pasteHintLine(m.width) != "" {
		t.Error("the hint must step aside while the preview is open")
	}
	m = ctrlO(t, m) // collapse again

	// Two tokens: the hint says which one opens.
	m = apply(t, m, tea.PasteMsg{Content: pasteLines(3)})
	if got := stripTCardANSI(m.pasteHintLine(m.width)); !strings.Contains(got, "of 2 pastes") {
		t.Errorf("two-paste hint = %q, want it to say which paste opens", got)
	}

	// Deleting the tokens takes the hint with them.
	m.input.Clear()
	if got := m.pasteHintLine(m.width); got != "" {
		t.Errorf("cleared draft = %q, want no hint", got)
	}
}

// TestPasteHintAbsentMidRun: the composer is a queue box while a run is in
// flight and ctrl+o does nothing there, so the hint must not advertise it.
func TestPasteHintAbsentMidRun(t *testing.T) {
	m := pasteInto(t, pasteLines(5))
	m.running = true
	if got := m.pasteHintLine(m.width); got != "" {
		t.Errorf("mid-run hint = %q, want none", got)
	}
}

// TestPastePreviewReservesRows pins the layout contract: the composer's extra
// rows come out of the transcript's budget, so it never overlaps the
// conversation or the status bar. Opening the preview REPLACES the hint row
// (the footer carries the keys once it is open), which is why the delta is the
// preview's height minus the hint's one row.
func TestPastePreviewReservesRows(t *testing.T) {
	m := pasteInto(t, pasteLines(20))
	if hint := m.pasteHintLine(m.width); hint == "" {
		t.Fatal("a collapsed paste should carry the ctrl+o hint")
	}
	before := m.transcript.viewportHeight()
	m = ctrlO(t, m)
	want := len(m.pastePreviewLines(m.width))
	if want == 0 {
		t.Fatal("no preview rows rendered")
	}
	if got := before - m.transcript.viewportHeight(); got != want-1 {
		t.Errorf("transcript shrank by %d rows, want %d (the preview's height less the hint row it replaced)",
			got, want-1)
	}
}

// TestPastePreviewTargetsCaret pins the targeting rule: the caret inside a token
// wins, and with the caret elsewhere the newest token in the draft is opened.
// The caret offset is exact because textarea's Column() is a rune index, so a
// token on the same line as the caret is found without any wrap reconstruction.
func TestPastePreviewTargetsCaret(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.pastes = map[int]string{1: "aaa", 2: "bbb"}
	m.input.SetValue("x [Pasted text #1 · 3 B] y [Pasted text #2 · 3 B]")
	m.input.ta.SetCursorColumn(strings.Index(m.input.Value(), "#1"))
	if got := m.pasteAtCursor(); got != 1 {
		t.Fatalf("pasteAtCursor = %d, want 1 (the caret sits inside token #1)", got)
	}
	m = ctrlO(t, m)
	if m.pastePreview == nil || m.pastePreview.id != 1 {
		t.Fatalf("preview = %+v, want the token under the caret", m.pastePreview)
	}

	// Caret away from every token: the newest one is the fallback.
	m.pastePreview = nil
	m.input.ta.SetCursorColumn(0)
	if got := m.pasteAtCursor(); got != 0 {
		t.Fatalf("pasteAtCursor = %d, want 0 with the caret outside every token", got)
	}
	m = ctrlO(t, m)
	if m.pastePreview == nil || m.pastePreview.id != 2 {
		t.Fatalf("preview = %+v, want the newest token", m.pastePreview)
	}
}

// TestPastePreviewInvalidatesWithDraft pins the lifecycle: dropping the draft
// (Ctrl+C's first stage) closes the preview, and so does submitting.
func TestPastePreviewInvalidatesWithDraft(t *testing.T) {
	m := pasteInto(t, pasteLines(5))
	m = ctrlO(t, m)
	if m.pastePreview == nil {
		t.Fatal("setup: preview did not open")
	}
	m = m.clearDraft()
	if m.pastePreview != nil {
		t.Errorf("clearing the draft must close the preview, got %+v", m.pastePreview)
	}

	// And editing the token out of the draft closes it on the next layout pass.
	m = pasteInto(t, pasteLines(5))
	m = ctrlO(t, m)
	m.input.SetValue("no tokens here any more")
	m.relayout()
	if m.pastePreview != nil {
		t.Errorf("deleting the token must close the preview, got %+v", m.pastePreview)
	}

	// Submitting consumes the pastes and the preview with them.
	m = pasteInto(t, pasteLines(5))
	m = ctrlO(t, m)
	next, _ := m.submit()
	m = next.(Model)
	if m.pastePreview != nil || len(m.pastes) != 0 {
		t.Errorf("submit must consume the preview and the bodies, got preview=%+v pastes=%d",
			m.pastePreview, len(m.pastes))
	}
}

// TestPastePreviewRendersInsideComposer pins the placement: the rows land in the
// composer's own frame (below the draft, above the bottom rule), not in an
// overlay above it.
func TestPastePreviewRendersInsideComposer(t *testing.T) {
	m := pasteInto(t, pasteLines(6))
	before := stripTCardANSI(m.View().Content)
	if strings.Contains(before, "line 01") {
		t.Fatalf("collapsed card should not show the body:\n%s", before)
	}
	m = ctrlO(t, m)
	after := stripTCardANSI(m.View().Content)
	if !strings.Contains(after, "line 01") || !strings.Contains(after, "ctrl+o collapse") {
		t.Fatalf("expanded composer should carry the body and its footer:\n%s", after)
	}
	// The draft row (the token) is still there, untouched above the body.
	if !strings.Contains(after, "[Pasted text #1") {
		t.Errorf("the draft token must survive the preview:\n%s", after)
	}
}
