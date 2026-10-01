package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
)

// This file implements the scrolling transcript region of the full-screen TUI
// (US-005, SPEC 5.1 transcript, FR-5/FR-10). The transcript owns a
// viewport.Model and an ordered list of rendered blocks (user / assistant /
// system turns). Streaming assistant text arrives as textDeltaMsg values that
// append to the current assistant block; turnEndMsg finalizes it. Content is
// re-flowed through the viewport with theme.WrapToWidth at the live width so CJK
// and emoji never split mid-rune. Tool cards are a later node (#389); this file
// leaves a clean seam (system lines) without building cards.

// blockRole distinguishes the three transcript block kinds so each renders with
// its own theme style.
type blockRole int

const (
	roleUser blockRole = iota
	roleAssistant
	roleSystem
	roleTool
	// roleBanner is the startup logo + config splash. Its text is pre-rendered
	// (already colored, already laid out) and emitted verbatim, so reflow neither
	// wraps it nor overrides its colors with a role style.
	roleBanner
)

// transcriptBlock is one rendered turn in the transcript. text is the raw
// (unstyled, unwrapped) message body; the role selects the theme style and any
// prefix applied at render time. For roleTool blocks text is unused and card
// points at the live tool card (#389); the pointer lets a later toolEndMsg /
// Ctrl+T mutate the card in place and have it re-render on the next reflow.
type transcriptBlock struct {
	role blockRole
	text string
	card *toolCard
}

// transcript is the scrolling message log. It wraps a viewport.Model and keeps
// the source blocks so it can re-flow on width changes. activeAssistant indexes
// the assistant block currently receiving streaming deltas, or -1 when no turn
// is streaming.
type transcript struct {
	vp    viewport.Model
	theme Theme

	// totalWidth is the full width the transcript may occupy (terminal columns
	// minus any chrome the model reserves). width (below) is the content width
	// the blocks actually wrap to: it equals totalWidth when the content fits, or
	// totalWidth-1 when it overflows and a scrollbar column must be held back.
	// reflow recomputes width from totalWidth on every content change, so the bar
	// column appears/disappears correctly even as a run streams in new lines.
	totalWidth int

	// width is the content width (terminal columns) the blocks wrap to. It is
	// separate from the viewport's own width so reflow measurements stay stable
	// even before the first size message.
	width int

	blocks          []transcriptBlock
	activeAssistant int

	// sealedThisTurn records that a live tool-call announcement split the
	// streaming assistant text this turn (codex ordering: call row above its
	// result). finalizeTurn then keeps the streamed deltas instead of
	// overwriting the post-card block with the full message text (which would
	// duplicate the pre-card text). Reset on every new user turn.
	sealedThisTurn bool

	// lines caches the last renderAll split: the transcript's full content lines
	// in order. Mouse selection endpoints anchor into these indices (see
	// selection), so scrolling preserves the highlight instead of clearing it;
	// streaming only appends, keeping earlier indices stable.
	lines []string

	// follow is the stick-to-bottom intent: while true, every reflow snaps the
	// viewport to the newest line so streamed output stays visible. It is set
	// when the user submits a turn and cleared when they scroll up to read
	// history (re-armed when they scroll back to the bottom). Tracking intent
	// explicitly — rather than sampling viewport.AtBottom() inside reflow — keeps
	// auto-scroll correct across height changes (setSize resizes the viewport
	// before reflow runs, which would make an AtBottom() sample read false).
	follow bool
}

// newTranscript builds an empty transcript with the given theme. The viewport
// starts zero-sized; the model drives setSize from the first tea.WindowSizeMsg.
func newTranscript(theme Theme) transcript {
	vp := viewport.New()
	return transcript{
		vp:              vp,
		theme:           theme,
		activeAssistant: -1,
	}
}

// reset clears every block while keeping the viewport, theme, and measured
// widths, so a session switch replays into a clean view without losing the
// terminal geometry. Callers re-add the banner and seed history after.
func (t *transcript) reset() {
	vp, theme, totalWidth, width := t.vp, t.theme, t.totalWidth, t.width
	*t = transcript{vp: vp, theme: theme, totalWidth: totalWidth, width: width, activeAssistant: -1, follow: true}
}

// setSize resizes the transcript's viewport and re-flows the blocks to the new
// width. A non-positive dimension is clamped to zero so the viewport never sees
// a negative extent. width is the total space available; reflow decides whether
// to spend one column on the scrollbar based on whether the content overflows.
func (t *transcript) setSize(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	t.totalWidth = width
	t.vp.SetHeight(height)
	t.reflow()
}

// addUser appends a user turn and closes any streaming assistant block, then
// re-flows. Submitting a prompt is an explicit action where the user always
// wants to see their new turn and the response that follows, so it re-arms
// follow: the viewport snaps to the bottom even if the user had scrolled up
// (e.g. reading the startup banner) — otherwise the streamed reply would
// accumulate off-screen and look like nothing happened. Subsequent streaming
// deltas keep the bottom via follow, which the user can pause by scrolling up.
func (t *transcript) addUser(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleUser, text: text})
	t.activeAssistant = -1
	t.sealedThisTurn = false
	t.follow = true
	t.reflow()
}

// addSystem appends a system / meta notice (used for run lifecycle and other
// inline notes).
func (t *transcript) addSystem(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleSystem, text: text})
	t.reflow()
}

// addBanner appends a pre-rendered splash block (startup logo + config). It is
// emitted verbatim by renderBlock, so its colors and horizontal layout survive
// reflow untouched.
func (t *transcript) addBanner(text string) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleBanner, text: text})
	t.reflow()
}

// addToolCard appends a rich tool-call card (#389) as an ordered block so it
// renders inline in the transcript. The card is held by pointer, so a later
// state change (toolEndMsg) or expand toggle (Ctrl+T) followed by reflow
// re-renders it in place.
func (t *transcript) addToolCard(c *toolCard) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c})
	t.reflow()
}

// announceToolCard inserts a live tool-call card at the current transcript end
// while a turn is still streaming: the in-progress assistant text block is
// sealed (later deltas open a fresh block after the card), so the call row
// renders above the text it produces — the codex call/result order — instead
// of all cards landing after the finished answer.
func (t *transcript) announceToolCard(c *toolCard) {
	if t.activeAssistant >= 0 && t.blocks[t.activeAssistant].text != "" {
		t.sealedThisTurn = true
		t.activeAssistant = -1
	}
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c})
	t.reflow()
}

// appendDelta grows the current assistant block by delta, creating the block on
// the first delta of a turn. The re-flow auto-sticks to the bottom when the user
// has not scrolled up.
func (t *transcript) appendDelta(delta string) {
	if t.activeAssistant < 0 {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant})
		t.activeAssistant = len(t.blocks) - 1
	}
	t.blocks[t.activeAssistant].text += delta
	t.reflow()
}

// finalizeTurn closes the streaming assistant block. When the final message
// carries text it becomes the block's authoritative body (covering turns that
// arrive without incremental deltas); otherwise the accumulated deltas stand.
func (t *transcript) finalizeTurn(msg agentcore.AssistantMessage) {
	text := agentcore.ContentToText(msg.Content)
	if t.activeAssistant >= 0 {
		// A split turn keeps its streamed deltas: overwriting the post-card
		// block with the full text would duplicate the sealed pre-card text.
		if text != "" && !t.sealedThisTurn {
			t.blocks[t.activeAssistant].text = text
		}
	} else if text != "" {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant, text: text})
	}
	t.activeAssistant = -1
	t.sealedThisTurn = false
	t.reflow()
}

// update forwards a message (typically a key press or scroll) to the viewport so
// PgUp/PgDn/arrow scrolling works, then re-syncs the follow intent: scrolling up
// off the bottom pauses auto-scroll, and scrolling back to the bottom re-arms it.
func (t *transcript) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.vp, cmd = t.vp.Update(msg)
	t.follow = t.vp.AtBottom()
	return cmd
}

// scrollToRow positions the viewport so the scrollbar thumb aligns with the
// given viewport row y (0-based). It is the inverse of the thumb-position math
// in scrollbar(): pressing or dragging on row y maps that row to the matching
// scroll offset, so clicking the gutter jumps there and dragging the thumb
// tracks the cursor. It is a no-op when the content fits (nothing to scroll).
func (t *transcript) scrollToRow(y int) {
	h := t.vp.Height()
	if h <= 0 {
		return
	}
	total := t.vp.TotalLineCount()
	if total <= h {
		return
	}
	thumb := h * h / total
	if thumb < 2 {
		thumb = 2
	}
	if thumb > h {
		thumb = h
	}
	span := h - thumb // rows the thumb top can occupy
	if span <= 0 {
		return
	}
	// Center the grab on the thumb: aim its top at y minus half its body so the
	// cursor sits roughly mid-thumb, then clamp into the track.
	top := y - thumb/2
	if top < 0 {
		top = 0
	}
	if top > span {
		top = span
	}
	maxOff := total - h
	t.vp.SetYOffset(top * maxOff / span)
	t.follow = t.vp.AtBottom()
}

// viewportHeight reports the number of visible transcript rows, so the model can
// tell whether a mouse Y falls within the scrollable region.
func (t transcript) viewportHeight() int { return t.vp.Height() }

// overflowing reports whether the transcript has more content than fits in the
// viewport, i.e. there is history to scroll. relayout uses this to reserve the
// scrollbar column only when scrolling is possible, and view uses it to decide
// whether to attach the thumb at all.
func (t transcript) overflowing() bool {
	return t.vp.Height() > 0 && t.vp.TotalLineCount() > t.vp.Height()
}

// view renders the current visible slice of the transcript. When the content
// overflows the viewport a one-column vertical scrollbar is drawn down the right
// edge (FR-10): each viewport row is normalized to exactly the content width
// before the scrollbar cell is appended, so the bar sits flush against the
// terminal's right edge and a dangling SGR from Markdown rendering can never
// bleed into (and hide) the bar column. When everything fits there is nothing to
// scroll, so no bar is drawn and the viewport uses the full width (relayout
// releases the reserved column in that case).
func (t transcript) view() string {
	if !t.overflowing() {
		return t.vp.View()
	}

	bar := strings.Split(t.scrollbar(), "\n")
	body := strings.Split(t.vp.View(), "\n")

	// Fit every body line to exactly t.width columns (ANSI-aware pad/truncate),
	// terminating any open style so the bar cell renders on a clean slate.
	fit := lipgloss.NewStyle().Width(t.width).MaxWidth(t.width)

	var b strings.Builder
	for i := 0; i < len(bar); i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := ""
		if i < len(body) {
			line = body[i]
		}
		if t.width > 0 {
			b.WriteString(fit.Render(line))
		}
		b.WriteString(bar[i])
	}
	return b.String()
}

// scrollLines moves the viewport by n content lines (negative scrolls up)
// without touching the selection: it is the drag-autoscroll path used while a
// mouse selection is pinned at the transcript edge. Follow intent is re-synced
// from the new position (reaching the bottom re-arms stick-to-bottom) so a
// drag that ends at the bottom leaves the transcript pinned. It returns false
// when already clamped at that end (no movement), so the caller can stop the
// tick loop instead of spinning forever.
func (t *transcript) scrollLines(n int) bool {
	before := t.vp.YOffset()
	if n > 0 {
		t.vp.ScrollDown(n)
	} else if n < 0 {
		t.vp.ScrollUp(-n)
	}
	t.follow = t.vp.AtBottom()
	return t.vp.YOffset() != before
}

// scrollbar renders the one-column vertical scrollbar the height of the
// viewport. A proportional thumb marks the visible window and its position marks
// the scroll offset, so scrolling up through history moves the thumb; the
// remaining rows draw a thin groove (│). The thumb is drawn as a capsule like
// the macOS system scrollbar: a lower-half block ▄ caps the top and an upper-half
// block ▀ caps the bottom (their filled halves sit on the inner edges so the
// outer ends taper to rounded), with the full block █ filling the body rows
// between the caps. The thumb is never shorter than three rows, so the capsule
// always shows a body between its two rounded caps rather than collapsing to a
// flat blob. When the content fits (no overflow) the capsule fills the full
// height.
func (t transcript) scrollbar() string {
	h := t.vp.Height()
	if h <= 0 {
		return ""
	}
	total := t.vp.TotalLineCount()
	thumb := h
	pos := 0
	if total > h {
		thumb = h * h / total
		// Keep the capsule shape (rounded cap + body + rounded cap) by never
		// letting the thumb shrink below three rows; clamp down to the viewport
		// height when it is shorter than that.
		if thumb < 3 {
			thumb = 3
		}
		if thumb > h {
			thumb = h
		}
		maxOff := total - h
		off := t.vp.YOffset()
		if off > maxOff {
			off = maxOff
		}
		if maxOff > 0 {
			pos = off * (h - thumb) / maxOff
		}
	}
	var b strings.Builder
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case i < pos || i >= pos+thumb:
			b.WriteString(t.theme.ScrollTrack.Render("│"))
		case thumb >= 2 && i == pos:
			b.WriteString(t.theme.ScrollThumb.Render("▄"))
		case thumb >= 2 && i == pos+thumb-1:
			b.WriteString(t.theme.ScrollThumb.Render("▀"))
		default:
			b.WriteString(t.theme.ScrollThumb.Render("█"))
		}
	}
	return b.String()
}

// reflow re-renders every block to the current width and pushes the joined
// content into the viewport. When the follow intent is set it snaps to the
// bottom so new content auto-scrolls; otherwise the offset is preserved so
// reading history is not interrupted. follow is tracked in update/scrollToRow
// (user scroll) and addUser (new turn) rather than sampled here, because setSize
// resizes the viewport before reflow runs and an AtBottom() sample would misread.
//
// Width is decided here rather than in setSize so it stays correct as a run
// streams in new lines (which reach reflow via appendDelta/finalizeTurn, not
// setSize): the blocks are first laid out at the full width, and only if that
// overflows the viewport is one column handed back to the scrollbar and the
// blocks re-laid at totalWidth-1. When the content fits, the transcript keeps
// the full width and view() draws no bar.
func (t *transcript) reflow() {
	t.width = t.totalWidth
	t.vp.SetWidth(t.width)
	t.setContent(t.renderAll())

	// A narrower width never reduces the line count, so if the full-width layout
	// already overflows it still overflows at totalWidth-1: reserve the scrollbar
	// column and re-lay the blocks so the body never sits under the bar.
	if t.totalWidth > 0 && t.vp.TotalLineCount() > t.vp.Height() {
		t.width = t.totalWidth - 1
		t.vp.SetWidth(t.width)
		t.setContent(t.renderAll())
	}

	if t.follow {
		t.vp.GotoBottom()
	}
}

// setContent pushes the full render into the viewport and caches its lines for
// content-anchored mouse selection (see lines).
func (t *transcript) setContent(rendered string) {
	t.vp.SetContent(rendered)
	t.lines = strings.Split(rendered, "\n")
}

// contentLines returns the cached full-content lines the selection anchors
// into. Empty before the first reflow.
func (t *transcript) contentLines() []string { return t.lines }

// renderAll joins every block, rendered to the current content width, into the
// transcript body string. Consecutive turns are separated by a blank line before
// a new user turn so requests read as visually distinct.
func (t *transcript) renderAll() string {
	var b strings.Builder
	for i, blk := range t.blocks {
		if i > 0 {
			b.WriteByte('\n')
			if blk.role == roleUser {
				b.WriteByte('\n')
			}
		}
		b.WriteString(t.renderBlock(blk, i == t.activeAssistant))
	}
	return b.String()
}

// renderBlock wraps a block's text to the content width and applies the role's
// theme style. Wrapping happens on the raw text (measured in display columns via
// WrapToWidth) before styling so ANSI escapes never confuse the width math and
// no double-width rune is split. A finalized assistant block is rendered as
// Markdown (fix #3, mirroring the REPL's turn-end render); the still-streaming
// block (streaming==true) stays plain text because Markdown can only be laid out
// once the whole block is known.
func (t transcript) renderBlock(blk transcriptBlock, streaming bool) string {
	if blk.role == roleTool && blk.card != nil {
		return blk.card.render(t.theme, t.width)
	}
	switch blk.role {
	case roleBanner:
		return blk.text
	case roleUser:
		return renderUserBlock(t.theme, blk.text, t.width)
	case roleSystem:
		return t.theme.System.Render(WrapToWidth(blk.text, t.width))
	default:
		if streaming {
			return t.theme.Assistant.Render(WrapToWidth(blk.text, t.width))
		}
		return renderMarkdown(blk.text, t.width)
	}
}

// renderUserBlock renders a user turn as a visually distinct block: every
// wrapped line carries a `› ` gutter in the User style (bold bright-white)
// so prompts never blend into assistant replies or dim system notes. Wrapping
// accounts for the 2-cell gutter so CJK/emoji never overflow the transcript
// width. This mirrors codex's `User ›` history cell: the speaker is
// obvious at a glance, while assistant text stays gutter-free.
func renderUserBlock(theme Theme, text string, width int) string {
	avail := width - 2
	if avail < 1 {
		avail = 1
	}
	wrapped := WrapToWidth(text, avail)
	lines := splitLines(wrapped)
	for i, ln := range lines {
		lines[i] = theme.User.Render("› " + ln)
	}
	return joinLines(lines)
}

// splitLines splits on newline without trimming, keeping empty lines so
// multi-paragraph prompts survive the gutter pass.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

// joinLines is strings.Join(lines, "\n") without importing strings at the
// call site (transcript.go already imports strings, but keep the helper
// dependency-free for tests).
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	b := make([]byte, 0, n)
	for i, l := range lines {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, l...)
	}
	return string(b)
}
