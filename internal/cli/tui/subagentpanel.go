package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// This file renders the multi-line sub-agent status panel (SPEC 4.4, US-006): a
// block shown just above the working spinner while one or more sub-agents
// dispatched by the `task` tool are running. Each active sub-agent contributes
// exactly one status line of the form:
//
//	⏺ {desc} · {activity} ({elapsed} · ↓{tokens})
//
// The panel is also interactive: while the input box is empty, ↑/↓ move a
// selection cursor over the rows and Enter expands the selected row to show that
// sub-agent's accumulated text output inline (below its status line), Esc
// collapses. The panel is a pure function of the model's ordered active-subagent
// set plus its selection state; it is re-rendered every spinner tick so the
// elapsed clock stays live without a dedicated timer. When there are no active
// sub-agents it renders nothing (zero lines, zero height), leaving the existing
// single-run layout untouched.

// maxExpandedLines caps how many wrapped output lines an expanded row shows. The
// output can grow without bound, so only the most recent lines are kept visible;
// older content scrolls off the top of the inline pane.
const maxExpandedLines = 12

// subagentRow is one live sub-agent's status, keyed by the parent task tool-call
// id. start is recorded when the row is added so elapsed can be computed at
// render time; activity/tokens are refreshed by subagentProgressMsg; output
// accumulates the sub-agent's forwarded text (toolUpdate deltas + final result)
// for the inline expanded view.
type subagentRow struct {
	id       string
	desc     string
	activity string
	tokens   int
	start    time.Time
	output   string
}

// subagentPanel is the ordered set of live sub-agents. order preserves insertion
// order (so rows render stably, oldest first) while byID gives O(1) lookup for
// progress updates and removal. selecting reports whether a row is cursored;
// selected is that row's index into order (meaningful only while selecting is
// true); expanded reports whether the selected row shows its output inline. The
// zero value is a valid empty, unselected panel — selecting defaults false so the
// selected int's zero value never spuriously marks row 0.
type subagentPanel struct {
	order     []string
	byID      map[string]*subagentRow
	selecting bool
	selected  int
	expanded  bool
}

// add records a newly dispatched sub-agent (a toolStartMsg with name=="task").
// It is idempotent on the id: a duplicate start refreshes the description and
// resets the start clock rather than adding a second row.
func (p *subagentPanel) add(id, desc string, now time.Time) {
	if p.byID == nil {
		p.byID = make(map[string]*subagentRow)
	}
	if row, ok := p.byID[id]; ok {
		row.desc = desc
		row.start = now
		return
	}
	p.byID[id] = &subagentRow{id: id, desc: desc, start: now}
	p.order = append(p.order, id)
}

// update folds a progress event into the row for id, refreshing its activity and
// token estimate. A progress for an unknown id (late/out-of-order, arriving
// before or without a start) adds the row so no update is lost; now seeds its
// start clock in that case.
func (p *subagentPanel) update(id, desc, activity string, tokens int, now time.Time) {
	if p.byID == nil {
		p.byID = make(map[string]*subagentRow)
	}
	row, ok := p.byID[id]
	if !ok {
		row = &subagentRow{id: id, desc: desc, start: now}
		p.byID[id] = row
		p.order = append(p.order, id)
	}
	if activity != "" {
		row.activity = activity
	}
	if desc != "" {
		row.desc = desc
	}
	row.tokens = tokens
}

// appendOutput accumulates a forwarded text delta into the row for id, so the
// expanded view can show the sub-agent's running output. Deltas for an unknown id
// are ignored (the row's start/end brackets its output; nothing to attach to).
func (p *subagentPanel) appendOutput(id, delta string) {
	if delta == "" {
		return
	}
	if row, ok := p.byID[id]; ok {
		row.output += delta
	}
}

// remove drops the row for id (the task's toolEndMsg). It is a no-op when id is
// absent, so an end without a matching start — or a duplicate end — is safe.
// The selection is clamped to the shrunken order so the cursor never dangles past
// the end; removing the last row clears the selection entirely.
func (p *subagentPanel) remove(id string) {
	if _, ok := p.byID[id]; !ok {
		return
	}
	delete(p.byID, id)
	for i, v := range p.order {
		if v == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	if len(p.order) == 0 {
		p.clearSelection()
		return
	}
	if p.selected >= len(p.order) {
		p.selected = len(p.order) - 1
	}
}

// active reports the number of live sub-agents (status rows the panel would
// render), ignoring any extra rows an expanded row contributes.
func (p *subagentPanel) active() int { return len(p.order) }

// hasSelection reports whether a row is currently cursored.
func (p *subagentPanel) hasSelection() bool {
	return p.selecting && p.selected >= 0 && p.selected < len(p.order)
}

// clearSelection drops the cursor and collapses any expansion.
func (p *subagentPanel) clearSelection() {
	p.selecting = false
	p.selected = 0
	p.expanded = false
}

// selectUp moves the cursor to the previous row. With no current selection the
// first press lands on the last (bottom-most) row; moving up collapses any open
// expansion so it re-anchors to the newly selected row.
func (p *subagentPanel) selectUp() {
	if len(p.order) == 0 {
		return
	}
	if !p.selecting {
		p.selecting = true
		p.selected = len(p.order) - 1
	} else if p.selected > 0 {
		p.selected--
	}
	p.expanded = false
}

// selectDown moves the cursor to the next row. With no current selection the
// first press lands on the first (top-most) row; moving down collapses any open
// expansion so it re-anchors to the newly selected row.
func (p *subagentPanel) selectDown() {
	if len(p.order) == 0 {
		return
	}
	if !p.selecting {
		p.selecting = true
		p.selected = 0
	} else if p.selected < len(p.order)-1 {
		p.selected++
	}
	p.expanded = false
}

// toggleExpand flips the expanded state of the selected row. It is a no-op when
// nothing is selected.
func (p *subagentPanel) toggleExpand() {
	if p.hasSelection() {
		p.expanded = !p.expanded
	}
}

// expandedID returns the id of the currently expanded row, or "" when no row is
// expanded. It lets the model relayout only when a streamed delta lands on the
// row whose inline output pane is on screen.
func (p *subagentPanel) expandedID() string {
	if p.expanded && p.hasSelection() {
		return p.order[p.selected]
	}
	return ""
}

// lineCount reports how many terminal rows the panel occupies at the given width:
// one status line per active sub-agent, plus the wrapped output lines when the
// selected row is expanded. relayout uses this to reserve exactly the right
// height so the transcript never overlaps the panel.
func (p subagentPanel) lineCount(width int) int {
	if len(p.order) == 0 || width <= 0 {
		return 0
	}
	n := len(p.order)
	if p.expanded && p.hasSelection() {
		if row := p.byID[p.order[p.selected]]; row != nil {
			n += len(p.expandedLines(row, width))
		}
	}
	return n
}

// view renders the panel to a string, one status line per active sub-agent in
// insertion order, each truncated to width display columns. The selected row is
// marked with a leading cursor and, when expanded, its accumulated output is
// rendered on the following (indented, wrapped) lines. It returns "" when there
// are no active sub-agents or width is non-positive, so an empty panel
// contributes zero rows and zero height. now is the reference time elapsed is
// measured from (the spinner tick's time) so the clock advances each frame.
func (p subagentPanel) view(theme Theme, width int, now time.Time) string {
	if len(p.order) == 0 || width <= 0 {
		return ""
	}
	lines := make([]string, 0, len(p.order)+1)
	for i, id := range p.order {
		row := p.byID[id]
		if row == nil {
			continue
		}
		cursored := p.selecting && i == p.selected
		lines = append(lines, TruncateToWidth(row.render(theme, now, cursored), width))
		if cursored && p.expanded {
			for _, out := range p.expandedLines(row, width) {
				lines = append(lines, theme.System.Render(out))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// expandedLines builds the wrapped, indented, tail-capped output lines shown
// under an expanded row. An empty output yields a single placeholder line so the
// pane is never blank. Lines are already truncated to width (as plain text); the
// caller styles them.
func (p subagentPanel) expandedLines(row *subagentRow, width int) []string {
	const indent = "  "
	out := strings.TrimRight(row.output, "\n")
	if out == "" {
		return []string{indent + "(no output yet)"}
	}
	var wrapped []string
	for _, para := range strings.Split(out, "\n") {
		for _, seg := range wrapToWidth(para, width-len(indent)) {
			wrapped = append(wrapped, indent+seg)
		}
	}
	if len(wrapped) > maxExpandedLines {
		wrapped = wrapped[len(wrapped)-maxExpandedLines:]
	}
	return wrapped
}

// wrapToWidth breaks s into segments no wider than width display columns,
// cutting on grapheme boundaries (there is no word-aware wrapping here —
// sub-agent output is arbitrary text/code). An empty line yields one empty
// segment so blank lines in the output are preserved.
//
// The string is decoded once into graphemes and escape sequences (ANSI-aware,
// never splitting an escape), so concatenating the segments reproduces the
// input byte for byte. One pass matters: this runs on every render tick for
// the whole accumulated output of an expanded sub-agent, and a
// Truncate/TruncateLeft pair per segment re-scanned the remainder each time,
// which is quadratic — a few tens of KB of output pinned a core for seconds
// (minutes under -race) on every frame.
//
// Open SGR attributes (colors among them) carry across a line break: the next
// segment re-opens exactly the sequences that were active at the split, so a
// wrapped colored line stays colored on every row, the way the previous
// implementation's escape re-injection did.
func wrapToWidth(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	if s == "" {
		return []string{""}
	}
	var segs []string
	var b strings.Builder
	var carried strings.Builder // SGR sequences active since the last full reset
	curWidth := 0
	var state byte // decoder state, zero value is NormalState
	pendingReplay := false
	// write appends text to the current segment, re-opening any carried SGR
	// state first so every segment after a wrap starts from the same
	// attributes as the source position it resumes at.
	write := func(text string) {
		if pendingReplay {
			pendingReplay = false
			if carried.Len() > 0 {
				b.WriteString(carried.String())
			}
		}
		b.WriteString(text)
	}
	for rest := s; rest != ""; {
		seq, w, n, newState := ansi.DecodeSequence(rest, state, nil)
		if n <= 0 {
			// Defensive: the decoder must always make progress. If it ever
			// did not, emit the remainder rather than spin.
			b.WriteString(rest)
			break
		}
		state = newState
		if seq != "" && seq[0] == '\x1b' && len(seq) > 2 && seq[1] == '[' && seq[len(seq)-1] == 'm' {
			sgrUpdate(&carried, seq)
		}
		if w > 0 && curWidth > 0 && curWidth+w > width {
			segs = append(segs, b.String())
			b.Reset()
			curWidth = 0
			pendingReplay = true
		}
		if w > width {
			// A single grapheme wider than the whole line (a CJK character at
			// width 1). Nothing that follows could fit either, so emit the
			// rest as the final segment and stop — the cap that keeps the
			// caller's line arithmetic finite.
			write(rest)
			segs = append(segs, b.String())
			return segs
		}
		write(seq)
		curWidth += w
		rest = rest[n:]
	}
	if b.Len() > 0 {
		segs = append(segs, b.String())
	}
	return segs
}

// sgrUpdate folds one SGR sequence into the carried style. A sequence that
// fully resets (empty params, or any 0 among them) drops the carried state;
// a reset that also sets attributes ("0;31") restarts from those; anything
// else accumulates, so replaying the carried list in order reproduces the
// exact attributes that were active at the split.
func sgrUpdate(carried *strings.Builder, seq string) {
	body := seq[2 : len(seq)-1]
	if body == "" {
		carried.Reset()
		return
	}
	reset, pure := false, true
	for _, f := range strings.Split(body, ";") {
		n, err := strconv.Atoi(f)
		if err != nil {
			pure = false // colon sub-parameters, private modes: keep as-is
			continue
		}
		if n == 0 {
			reset = true
		} else {
			pure = false
		}
	}
	if reset {
		carried.Reset()
		if pure {
			return
		}
	}
	carried.WriteString(seq)
}

// render builds one status line for a row: "{cursor}⏺ {desc} · {activity}
// ({elapsed} · ↓{tokens})". A blank description is omitted (the line leads with
// the glyph and activity); a zero token estimate drops the "↓" stat. When
// selected, the line leads with a "❯ " cursor and the head takes the accent color
// to stand out; otherwise the glyph + head take the spinner color and the
// parenthetical stats are dim, mirroring the spinner line.
func (r subagentRow) render(theme Theme, now time.Time, selected bool) string {
	var head strings.Builder
	head.WriteString("⏺")
	if r.desc != "" {
		fmt.Fprintf(&head, " %s ·", r.desc)
	}
	fmt.Fprintf(&head, " %s", r.activity)

	stats := formatElapsed(now.Sub(r.start))
	if r.tokens > 0 {
		stats += " · ↓" + humanizeInt(r.tokens)
	}

	headStyle := theme.Spinner
	cursor := "  "
	if selected {
		headStyle = theme.Accent
		cursor = theme.Accent.Render("❯ ")
	}
	return cursor + headStyle.Render(head.String()) + " " + theme.System.Render("("+stats+")")
}
