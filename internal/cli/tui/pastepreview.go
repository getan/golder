package tui

// This file implements the paste preview: a collapsed paste can be read (and
// paged through) without submitting it, inside the composer's own frame.
//
// The draft keeps nothing but the placeholder token; the preview is a VIEW
// concern appended inside the input box under the draft rows. Two reasons it is
// done this way rather than by putting the body back into the buffer:
//
//   - bubbles' textarea has no "set the caret to this offset" API (only
//     begin/end and visual-row moves), so a buffer swap would move the user's
//     caret every time they peeked. Render-time rows move nothing.
//   - the buffer stays exactly what gets submitted (a token expanded at submit
//     time), so every existing path — history, draft discard, expandPastes —
//     keeps working unchanged.
//
// Ctrl+O is the only entry point. A mouse click was considered and skipped: the
// composer has no hit-testing today (the tool-card click path covers the
// transcript only), and adding a column→offset mapping for a wrapped, styled
// editor is real machinery for a gesture the key already covers.

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/cli/ui"
)

// pasteCollapseChars is the single-line cutoff: a one-line paste longer than
// this collapses to a placeholder like a multi-line one does. A base64 blob, a
// minified bundle or a one-line log floods the composer exactly like a
// multiline paste, and used to sail straight in.
const pasteCollapseChars = 1000

// pastePreviewMaxRows bounds the expanded body: at most a third of the
// terminal, and never more than 8 rows. The transcript is the thing the user is
// working in; a preview is a glance, so it is capped well below the space a
// side panel would take.
func pastePreviewMaxRows(height int) int {
	if height <= 0 {
		return 8
	}
	rows := height / 3
	if rows > 8 {
		rows = 8
	}
	if rows < 2 {
		rows = 2
	}
	return rows
}

// pastePreview is the open preview's state: which paste is being read, and the
// first body line shown. It is nil while nothing is open.
type pastePreview struct {
	id int
	// top is the first visible body line; it is clamped against the body length
	// at render time, so a body that shrank (or a resize) cannot leave it in a
	// dead zone.
	top int
}

// pastePlaceholder renders the token left in the composer for a collapsed
// paste. It carries what the user needs to judge the paste without opening it:
// the id (so two pastes are distinguishable), the line count for a multi-line
// body, and the size. The count used to be lines-only, which said nothing about
// a one-line payload; the size says something about both shapes.
func pastePlaceholder(id int, body string) string {
	size := humanBytes(len(body))
	if lines := pasteLineCount(body); lines > 1 {
		return fmt.Sprintf("[Pasted text #%d · %d lines · %s]", id, lines, size)
	}
	return fmt.Sprintf("[Pasted text #%d · %s]", id, size)
}

// humanBytes formats a byte count for the placeholder: exact bytes below 1 KiB,
// one decimal above it.
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// pasteBodyLines splits a stored body into the rows the preview pages through.
// One trailing newline is dropped: a terminal selection almost always ends with
// one, it TERMINATES the last line rather than adding a blank one, and counting
// it turned a 42-line selection into "43 lines" — a number the user can see is
// wrong. A trailing BLANK line ("a\n\n") still shows: there the second newline
// is the empty line, and only the terminator goes.
func pasteBodyLines(body string) []string {
	return strings.Split(strings.TrimSuffix(body, "\n"), "\n")
}

// pasteLineCount is the line count the token reports and the preview pages
// against; it shares pasteBodyLines' rule so the two can never disagree.
func pasteLineCount(body string) int { return len(pasteBodyLines(body)) }

// pasteInDraft reports whether the draft still carries the token for id. A user
// who deletes the token (or the whole draft) has dismissed the paste as far as
// the composer is concerned, so the preview must not outlive it.
func (m Model) pasteInDraft(id int) bool {
	want := strconv.Itoa(id)
	for _, match := range pastePlaceholderRe.FindAllStringSubmatch(m.input.Value(), -1) {
		if match[1] == want {
			return true
		}
	}
	return false
}

// pasteTokenCount is how many collapsed paste tokens the draft holds.
func (m Model) pasteTokenCount() int {
	return len(pastePlaceholderRe.FindAllStringSubmatch(m.input.Value(), -1))
}

// pasteHintLine is the one dim row that tells the composer's reader that a
// collapsed paste can be read in place. Without it the paste is a dead end the
// user has no way to discover — the token names the body and the size, but
// nothing says a key opens it. It shows only while a token is in the draft and
// the preview is closed: the open preview's footer already carries the keys, and
// two rows saying "ctrl+o" would contradict each other.
func (m Model) pasteHintLine(width int) string {
	if m.running || m.pasteTokenCount() == 0 {
		return ""
	}
	if m.pastePreview != nil && m.pasteInDraft(m.pastePreview.id) {
		return ""
	}
	text := "ctrl+o previews it here"
	if n := m.pasteTokenCount(); n > 1 {
		text = fmt.Sprintf("ctrl+o previews the newest of %d pastes", n)
	}
	inner := width - 2
	if inner < 1 {
		return ""
	}
	if ui.Width(text) > inner {
		text = "ctrl+o previews it"
	}
	return m.theme.System.Render("  " + TruncateToWidth(text, inner))
}

// composerExtraLines returns the rows the composer frame carries below the
// draft: the preview while one is open, otherwise the hint that the preview
// exists. Both View and relayout call it, so the rows painted and the rows
// reserved cannot disagree.
func (m Model) composerExtraLines(width int) []string {
	if lines := m.pastePreviewLines(width); len(lines) > 0 {
		return lines
	}
	if hint := m.pasteHintLine(width); hint != "" {
		return []string{hint}
	}
	return nil
}

// newestPasteInDraft returns the highest paste id still present in the draft,
// or 0 when the draft has no token. It is the fallback target for Ctrl+O when
// the caret is not inside a token.
func (m Model) newestPasteInDraft() int {
	newest := 0
	for _, match := range pastePlaceholderRe.FindAllStringSubmatch(m.input.Value(), -1) {
		if id, err := strconv.Atoi(match[1]); err == nil && id > newest {
			newest = id
		}
	}
	return newest
}

// pasteAtCursor returns the paste whose token contains the caret, or 0. The
// mapping is exact because textarea's Column() is a RUNE index into the current
// logical line (see bubbles' SetCursorColumn, which clamps to len(value[row])),
// not a display column — so soft wrapping cannot make the two disagree.
func (m Model) pasteAtCursor() int {
	value := m.input.Value()
	row, col := m.input.Line(), m.input.Column()
	lines := strings.Split(value, "\n")
	if row < 0 || row >= len(lines) {
		return 0
	}
	offset := 0
	for _, line := range lines[:row] {
		offset += len(line) + 1 // the newline that separated it
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}
	offset += len(string(runes[:col]))

	for _, loc := range pastePlaceholderRe.FindAllStringIndex(value, -1) {
		// End-inclusive: the caret sitting right after a token (where it lands
		// after the paste) is "in" that token for this key's purpose.
		if offset >= loc[0] && offset <= loc[1] {
			match := pastePlaceholderRe.FindStringSubmatch(value[loc[0]:loc[1]])
			if len(match) > 1 {
				if id, err := strconv.Atoi(match[1]); err == nil {
					return id
				}
			}
		}
	}
	return 0
}

// togglePastePreview is Ctrl+O: open the paste the caret is in (else the newest
// one in the draft), switch to another token, or collapse what is open. With no
// paste in the draft it does nothing.
func (m Model) togglePastePreview() (tea.Model, tea.Cmd) {
	if m.running {
		// Mid-run the composer is a queue box; reading a paste there is not a
		// thing the user is doing with this key, and the preview rows would
		// fight the run chrome for the same budget.
		return m, nil
	}
	target := m.pasteAtCursor()
	if target == 0 {
		target = m.newestPasteInDraft()
	}
	if target == 0 {
		return m, nil
	}
	if m.pastePreview != nil && m.pastePreview.id == target {
		m.pastePreview = nil
	} else {
		m.pastePreview = &pastePreview{id: target}
	}
	m.relayout()
	return m, nil
}

// scrollPastePreview moves the open preview by dir * step body lines: a page
// for PgUp/PgDn, one line for the arrow keys. A step below 1 is treated as one
// line, so a bad caller cannot freeze the view.
func (m Model) scrollPastePreview(dir, step int) Model {
	p := m.pastePreview
	if p == nil {
		return m
	}
	body, ok := m.pastes[p.id]
	if !ok {
		m.pastePreview = nil
		return m
	}
	if step < 1 {
		step = 1
	}
	rows := pastePreviewMaxRows(m.height)
	maxTop := len(pasteBodyLines(body)) - rows
	if maxTop < 0 {
		maxTop = 0
	}
	p.top += dir * step
	if p.top > maxTop {
		p.top = maxTop
	}
	if p.top < 0 {
		p.top = 0
	}
	return m
}

// pastePreviewLines renders the open preview as composer rows: the body window
// followed by the position/keys footer. It returns nil when nothing is open, or
// when the paste it names is gone, so the caller can treat nil as "no rows".
//
// Long body lines are truncated rather than wrapped: one body line is one
// composer row, which keeps the row accounting exact (the model reserves
// len(lines) rows) and keeps the preview a stable shape while paging.
func (m Model) pastePreviewLines(width int) []string {
	p := m.pastePreview
	if p == nil || m.running {
		return nil
	}
	body, ok := m.pastes[p.id]
	if !ok || !m.pasteInDraft(p.id) {
		return nil
	}
	if width < 8 {
		return nil // too narrow to read or to account for honestly
	}
	inner := width - 2
	lines := pasteBodyLines(body)
	rows := pastePreviewMaxRows(m.height)
	top := p.top
	if maxTop := len(lines) - rows; top > maxTop {
		top = maxTop
	}
	if top < 0 {
		top = 0
	}
	end := top + rows
	if end > len(lines) {
		end = len(lines)
	}

	out := make([]string, 0, end-top+1)
	for _, line := range lines[top:end] {
		out = append(out, m.theme.System.Render("  "+TruncateToWidth(line, inner)))
	}
	footer := fmt.Sprintf("lines %d–%d of %d · ↑↓ scroll · PgUp/PgDn page · ctrl+o collapse", top+1, end, len(lines))
	if ui.Width(footer) > inner {
		// Same information, packed for a narrow pane.
		footer = fmt.Sprintf("%d–%d/%d · PgUp/PgDn · ctrl+o", top+1, end, len(lines))
	}
	out = append(out, m.theme.System.Render("  "+TruncateToWidth(footer, inner)))
	return out
}
