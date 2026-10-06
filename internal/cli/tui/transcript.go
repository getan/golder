package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
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
	// roleReview is a permission-gate verdict card: one line announcing an
	// automated approval, sandbox routing, denial, or read-only block, plus
	// the reviewer's rationale. It is styled by severity (warn/error).
	roleReview
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
	// note is set for roleReview blocks: the verdict determines the color
	// (denied/blocks red, approvals/containment amber).
	note *judge.Note
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

	// renderCache memoizes per-block renders for stable (sealed) blocks so a
	// streaming delta only pays for the live block, not a full-history glamour
	// pass. Keyed by block index (blocks are append-only; reset clears it).
	// A finished tool card is stable too: its entry is invalidated by the
	// card's revision, which every mutation bumps. The streaming assistant
	// block and running cards are never cached.
	renderCache map[int]cachedRender

	// batchDepth suspends reflow while a replay appends many blocks
	// (seedTranscript), so the transcript pays for one layout instead of the
	// O(n^2) cost of re-rendering the growing history per block. reflow calls
	// made while batching are dropped; endBatch lays everything out once.
	batchDepth int

	// barReserved records that the last layout overflowed the viewport and one
	// column was handed to the scrollbar. Starting the next layout at the
	// reserved width keeps the render cache's width key stable across reflows
	// (probing full width first would re-render every block at the other width
	// on each pass, defeating the cache).
	barReserved bool

	// lines caches the last renderAll split: the transcript's full content lines
	// in order. Mouse selection endpoints anchor into these indices (see
	// selection), so scrolling preserves the highlight instead of clearing it;
	// streaming only appends, keeping earlier indices stable.
	lines []string

	// cardSpans maps each tool card to its [first,last] content-line range in
	// the last renderAll, so a mouse click can be routed to the card under the
	// cursor: clicking a card toggles its expanded state (the codex-lacking
	// affordance that lets any card, not just the newest, be expanded).
	cardSpans map[*toolCard][2]int

	// follow is the stick-to-bottom intent: while true, every reflow snaps the
	// viewport to the newest line so streamed output stays visible. It is set
	// when the user submits a turn and cleared when they scroll up to read
	// history (re-armed when they scroll back to the bottom). Tracking intent
	// explicitly — rather than sampling viewport.AtBottom() inside reflow — keeps
	// auto-scroll correct across height changes (setSize resizes the viewport
	// before reflow runs, which would make an AtBottom() sample read false).
	follow bool

	// unseen counts content lines that arrived while follow was paused (the user
	// scrolled up). It drives the "N new lines · Ctrl+E" toast and resets
	// whenever the viewport is pinned back to the bottom (scroll-down,
	// jumpToBottom, or a new submitted turn re-arming follow).
	unseen int
}

// cachedRender is one memoized block render: the body plus the width it was
// wrapped to and, for tool cards, the card revision it was rendered from. A
// cache hit needs the same width and — for cards — that no mutation landed
// since.
type cachedRender struct {
	text  string
	width int
	rev   uint64
}

// newTranscript builds an empty transcript with the given theme. The viewport
// starts zero-sized; the model drives setSize from the first tea.WindowSizeMsg.
func newTranscript(theme Theme) transcript {
	vp := viewport.New()
	// Horizontal scrolling is disabled on purpose (codex parity): the
	// transcript lays every block out to the exact content width itself, so
	// there is nothing to pan to — letting the viewport track a horizontal
	// offset only lets a stray trackpad swipe (MouseWheelLeft/Right, or
	// Shift+wheel) slide the whole history sideways and hide the tool-card
	// bullets at the left edge. A zero horizontal step turns every horizontal
	// scroll path into a no-op while leaving vertical wheel/page scrolling
	// untouched.
	vp.SetHorizontalStep(0)
	return transcript{
		vp:              vp,
		theme:           theme,
		activeAssistant: -1,
		// Start pinned to the bottom: the launch banner and any replayed
		// history fill the viewport downward, and nothing is "waiting below
		// the fold" on a fresh screen — without this the very first reflow
		// would count the banner as unseen output and pop a phantom notice.
		follow: true,
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
	if width != t.totalWidth {
		// Cached block renders were wrapped to the old width.
		t.renderCache = nil
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

// setBannerText replaces the startup splash's pre-rendered text in place and
// drops its memoized render so the next reflow repaints it. The model uses this
// to advance the animated wordmark; keeping the block at the same index preserves
// every later block's render-cache key and the scroll/selection anchors that
// point into the rendered lines.
func (t *transcript) setBannerText(text string) {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		if t.blocks[i].role != roleBanner {
			continue
		}
		if t.blocks[i].text == text {
			return
		}
		t.blocks[i].text = text
		delete(t.renderCache, i)
		t.reflow()
		return
	}
}

// addToolCard appends a rich tool-call card (#389) as an ordered block so it
// renders inline in the transcript. The card is held by pointer, so a later
// state change (toolEndMsg) or expand toggle (Ctrl+T) followed by reflow
// re-renders it in place.
func (t *transcript) addToolCard(c *toolCard) {
	t.blocks = append(t.blocks, transcriptBlock{role: roleTool, card: c})
	t.reflow()
}

// addReviewNote files a permission-gate verdict card directly above the tool
// card it decided (the codex order: review line, then the call row). The card
// was announced while the call streamed, i.e. before the gate ran, so the note
// arrives later and is inserted at that card's index; when no matching card
// exists it appends.
func (t *transcript) addReviewNote(n judge.Note) {
	blk := transcriptBlock{role: roleReview, note: &n}
	if n.ToolCallID != "" {
		for i := len(t.blocks) - 1; i >= 0; i-- {
			b := t.blocks[i]
			if b.role == roleTool && b.card != nil && b.card.id == n.ToolCallID {
				// Inserting shifts every later block's index, so the
				// index-keyed render cache must be dropped.
				t.renderCache = nil
				t.blocks = slices.Insert(t.blocks, i, blk)
				t.reflow()
				return
			}
		}
	}
	t.blocks = append(t.blocks, blk)
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

// appendDeltaText grows the current assistant block by delta, creating the
// block on the first delta of a turn, without reflowing. High-rate provider
// deltas are coalesced by the model (see streamRenderTickMsg) so per-token
// reflows cannot pin the UI.
func (t *transcript) appendDeltaText(delta string) {
	if t.activeAssistant < 0 {
		t.blocks = append(t.blocks, transcriptBlock{role: roleAssistant})
		t.activeAssistant = len(t.blocks) - 1
	}
	t.blocks[t.activeAssistant].text += delta
}

// appendDelta grows the current assistant block by delta and re-flows in the
// same step. The re-flow auto-sticks to the bottom when the user has not
// scrolled up. Synchronous callers (tests, scripts) use this; the live run
// path appends with appendDeltaText and renders on its coalescing tick.
func (t *transcript) appendDelta(delta string) {
	t.appendDeltaText(delta)
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
	} else if text != "" && !t.sealedThisTurn {
		// No block is streaming and no deltas arrived for this turn: the text
		// has not been rendered yet, so add it. A sealed turn is the
		// exception — a live tool announcement already put this turn's
		// narration above its card, and the full message carries that same
		// text again; appending it here would print every command's narration
		// twice (once above the card, once below).
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
	t.syncFollow()
	return cmd
}

// syncFollow re-reads the viewport position into the follow intent and clears
// the unseen counter when the viewport is (back) at the bottom. Every scroll
// path funnels through here so "reached the bottom" always re-arms stick-to-
// bottom without leaving a stale "new lines" notice behind.
func (t *transcript) syncFollow() {
	t.follow = t.vp.AtBottom()
	if t.follow {
		t.unseen = 0
	}
}

// jumpToBottom re-arms stick-to-bottom and snaps the viewport to the newest
// line. It backs the Ctrl+E key after the user scrolled up: they return to the
// live tail immediately instead of hunting for the bottom. It is a no-op when
// the transcript is already pinned, apart from clearing the notice.
func (t *transcript) jumpToBottom() {
	t.follow = true
	t.unseen = 0
	t.vp.GotoBottom()
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
	t.syncFollow()
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
	t.syncFollow()
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
// setSize). The last layout's overflow state is remembered in barReserved: once
// the content overflows, the next reflow starts at totalWidth-1 directly, so
// the render cache stays keyed to one width per reflow instead of being
// invalidated by a full-width probe on every pass. The content only re-probes
// the full width when it fits again at totalWidth-1 (session switch, cleared
// transcript).
func (t *transcript) reflow() {
	if t.batchDepth > 0 {
		return
	}
	if t.totalWidth > 0 && t.barReserved {
		t.layoutAt(t.totalWidth - 1)
		if t.vp.TotalLineCount() <= t.vp.Height() {
			// The content no longer overflows: hand the column back.
			t.barReserved = false
			t.layoutAt(t.totalWidth)
		}
	} else {
		t.barReserved = false
		t.layoutAt(t.totalWidth)
		// A narrower width never reduces the line count, so an overflow at the
		// full width is final: reserve the scrollbar column and re-lay the
		// blocks so the body never sits under the bar.
		if t.totalWidth > 0 && t.vp.TotalLineCount() > t.vp.Height() {
			t.barReserved = true
			t.layoutAt(t.totalWidth - 1)
		}
	}

	if t.follow {
		t.vp.GotoBottom()
	}
}

// layoutAt lays every block out at width w and pushes the render into the
// viewport.
func (t *transcript) layoutAt(w int) {
	t.width = w
	t.vp.SetWidth(w)
	t.setContent(t.renderAll())
}

// beginBatch suspends reflow until endBatch so a bulk append (seedTranscript
// replaying hundreds of blocks) pays for one layout pass instead of one per
// block. Nesting is counted; only the outermost endBatch re-renders.
func (t *transcript) beginBatch() { t.batchDepth++ }

// endBatch closes the outermost batch and lays the transcript out once.
func (t *transcript) endBatch() {
	if t.batchDepth > 0 {
		t.batchDepth--
	}
	if t.batchDepth == 0 {
		t.reflow()
	}
}

// setContent pushes the full render into the viewport and caches its lines for
// content-anchored mouse selection (see lines).
func (t *transcript) setContent(rendered string) {
	before := t.vp.TotalLineCount()
	t.lines = strings.Split(rendered, "\n")
	// Hand the viewport the slice the transcript already split instead of the
	// raw string: SetContent would split the whole body a second time on every
	// streaming reflow. Neither side mutates the slice.
	t.vp.SetContentLines(t.lines)
	if t.follow {
		// Pinned: nothing can be waiting below the fold.
		t.unseen = 0
		return
	}
	// Only growth the viewport cannot show is "unseen". When everything still
	// fits (or the offset was clamped back to the end), AtBottom is true and
	// there is nothing below the fold to announce — counting it anyway is what
	// raised the phantom "N new lines" notice on startup, when the banner
	// itself briefly filled a not-yet-pinned viewport.
	if t.vp.AtBottom() {
		t.unseen = 0
		return
	}
	if after := t.vp.TotalLineCount(); after > before {
		// Approximate, but that is all the notice needs: line-count growth is a
		// faithful signal that new output landed while the user was reading
		// history (width re-wraps may over-count slightly; harmless).
		t.unseen += after - before
	}
}

// contentLines returns the cached full-content lines the selection anchors
// into. Empty before the first reflow.
func (t *transcript) contentLines() []string { return t.lines }

// renderAll joins every block, rendered to the current content width, into the
// transcript body string. User turns are separated by a blank line on both sides
// so the bar breathes like codex's history cell: a blank line before the bar
// and another blank line after it before the reply starts.
func (t *transcript) renderAll() string {
	if t.renderCache == nil {
		t.renderCache = map[int]cachedRender{}
	}
	t.cardSpans = map[*toolCard][2]int{}
	var b strings.Builder
	// line tracks the content-line index the next written block starts on, so
	// card spans can be recorded in the same pass that lays the blocks out.
	// A single "\n" separator between blocks only terminates the previous
	// line (no index change); the user-adjacency blank line advances it.
	line := 0
	for i, blk := range t.blocks {
		if i > 0 {
			b.WriteByte('\n')
			if blk.role == roleUser || t.blocks[i-1].role == roleUser {
				b.WriteByte('\n')
				line++
			}
		}
		// Stable blocks (everything except the live streaming assistant block
		// and running cards) render once and are reused: without this, every
		// streaming delta would re-run glamour over the whole history — and,
		// since expanded patch cards render colored diffs, re-render every
		// finished tool card too.
		s, ok := t.cachedRender(i, blk)
		if !ok {
			s = t.renderBlock(blk, i == t.activeAssistant)
			t.storeRender(i, blk, s)
		}
		if blk.role == roleTool && blk.card != nil {
			t.cardSpans[blk.card] = [2]int{line, line + strings.Count(s, "\n")}
		}
		b.WriteString(s)
		line += strings.Count(s, "\n") + 1
	}
	return b.String()
}

// cachedRender returns the memoized render for block i when it is still valid:
// the block must be stable, the entry must have been laid out at the current
// width, and — for a tool card — no mutation may have landed since it was
// stored (the card's revision changed).
func (t *transcript) cachedRender(i int, blk transcriptBlock) (string, bool) {
	if !stableBlock(blk, i == t.activeAssistant) {
		return "", false
	}
	e, ok := t.renderCache[i]
	if !ok || e.width != t.width {
		return "", false
	}
	if blk.role == roleTool && (blk.card == nil || e.rev != blk.card.rev) {
		return "", false
	}
	return e.text, true
}

// storeRender memoizes a block's render, or drops the entry when the block is
// not cacheable: the live streaming block's text is still growing, and a
// running card re-renders on every pass. Cards record the revision they were
// rendered from so a later mutation (completion, expand toggle) invalidates
// the entry.
func (t *transcript) storeRender(i int, blk transcriptBlock, s string) {
	if !stableBlock(blk, i == t.activeAssistant) {
		delete(t.renderCache, i)
		return
	}
	rev := uint64(0)
	if blk.role == roleTool && blk.card != nil {
		rev = blk.card.rev
	}
	t.renderCache[i] = cachedRender{text: s, width: t.width, rev: rev}
}

// toolCardAt returns the tool card whose rendered block covers content line
// index line, or nil when that line belongs to another block. It reads the
// spans recorded by the last renderAll, so it is only meaningful for clicks
// after the first reflow.
func (t transcript) toolCardAt(line int) *toolCard {
	for card, span := range t.cardSpans {
		if line >= span[0] && line <= span[1] {
			return card
		}
	}
	return nil
}

// stableBlock reports whether a block's render is immutable: user/system/banner
// turns and finalized assistant turns never change after they are sealed, and a
// finished tool card changes only when a mutation bumps its revision (the cache
// entry is keyed by it). The streaming assistant block and running cards must
// re-render every pass.
func stableBlock(blk transcriptBlock, streaming bool) bool {
	if streaming {
		return false
	}
	switch blk.role {
	case roleUser, roleSystem, roleBanner:
		return true
	case roleReview:
		return true
	case roleAssistant:
		return true
	case roleTool:
		return blk.card != nil && blk.card.state != cardRunning
	default:
		return false
	}
}

// renderBlock wraps a block's text to the content width and applies the role's
// theme style. Wrapping happens on the raw text (measured in display columns via
// WrapToWidth) before styling so ANSI escapes never confuse the width math and
// no double-width rune is split. Assistant blocks render as Markdown in BOTH
// phases (codex parity): the still-streaming block goes through the same
// renderMarkdown as the finalized one, so colors appear incrementally and the
// turn-end render is a no-op instead of a plain→styled flash. Partial Markdown
// (an unclosed fence or emphasis run) renders transiently and converges as more
// text arrives; glamour tolerates the prefix without error.
func (t *transcript) renderBlock(blk transcriptBlock, streaming bool) string {
	if blk.role == roleTool && blk.card != nil {
		return blk.card.render(t.theme, t.width)
	}
	switch blk.role {
	case roleReview:
		return t.renderReviewBlock(blk)
	case roleBanner:
		return blk.text
	case roleUser:
		return renderUserBlock(t.theme, blk.text, t.width)
	case roleSystem:
		return t.theme.System.Render(WrapToWidth(blk.text, t.width))
	default:
		return renderMarkdown(blk.text, t.width)
	}
}

// renderReviewBlock paints one verdict card: a warn color for approvals and
// containment (the call proceeds), an error color for denials and blocks. The
// line wraps to the transcript width; the icon leads.
func (t *transcript) renderReviewBlock(blk transcriptBlock) string {
	if blk.note == nil {
		return ""
	}
	style := t.theme.Warn
	if blk.note.Kind == judge.NoteDenied || blk.note.Kind == judge.NoteBlockedNoPrompt || blk.note.Kind == judge.NoteReadOnly {
		style = t.theme.Error
	}
	return style.Render(WrapToWidth(judge.FormatNote(*blk.note), t.width))
}

// renderUserBlock renders a user turn as a full-width bar like codex's history
// cell: only the first wrapped line carries the `› ` gutter, continuation lines
// are indented two cells to align, all painted on the User background and padded
// to the transcript width so the background spans edge to edge. A one-line
// vertical padding above and below makes the bar breathe instead of hugging the
// text, while assistant text stays bar-free.
func renderUserBlock(theme Theme, text string, width int) string {
	avail := width - 2
	if avail < 1 {
		avail = 1
	}
	wrapped := WrapToWidth(text, avail)
	lines := splitLines(wrapped)
	if width <= 0 {
		for i, ln := range lines {
			if i == 0 {
				lines[i] = theme.User.Render("› " + ln)
			} else {
				lines[i] = theme.User.Render("  " + ln)
			}
		}
		return joinLines(lines)
	}
	content := make([]string, len(lines))
	for i, ln := range lines {
		if i == 0 {
			content[i] = "› " + ln
		} else {
			content[i] = "  " + ln
		}
	}
	bar := theme.User.Width(width).MaxWidth(width).Padding(1, 0, 1, 0)
	return bar.Render(joinLines(content))
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
