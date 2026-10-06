package tui

import (
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
	// roleReview is a standalone permission-gate verdict: one line announcing
	// an automated approval, sandbox routing, denial, or read-only block, plus
	// the reviewer's rationale, styled by severity (warn/error). Verdicts whose
	// tool card is known render inside that card instead (see toolCard.notes);
	// this block role covers notes without a card in the transcript.
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

	// matStart and matEnd bound the materialized window [matStart, matEnd):
	// the blocks whose renders live in prefixLines. Blocks before matStart are
	// deferred history — a huge replay (resumed/imported session) renders only
	// its newest window up front and leaves the rest unrendered until the user
	// scrolls toward the top (extendSeed), so seed time and retained render
	// memory stay proportional to what is actually looked at. matEnd advances
	// as tail blocks become stable (commitStable); matStart is 0 whenever
	// nothing is deferred.
	matStart int
	matEnd   int

	// prefixLines is the materialized window rendered once and reused verbatim
	// by every later layout: the committed prefix that replaced the old
	// per-block render cache map (one contiguous rendered copy instead of one
	// retained string per block). prefixCounts records how many content lines
	// each window block contributed, so card hit-testing can address committed
	// cards without re-rendering them. prefixValid is cleared when the stored
	// wrap no longer matches the content (width change, in-place banner edit,
	// mid-history insertion); prefixWidth is the content width it was laid out
	// at.
	prefixLines  []string
	prefixCounts []int
	prefixValid  bool
	prefixWidth  int

	// batchDepth suspends reflow while a replay appends many blocks
	// (seedTranscript), so the transcript pays for one layout instead of the
	// O(n^2) cost of re-rendering the growing history per block. reflow calls
	// made while batching are dropped; endBatch lays everything out once.
	batchDepth int

	// barReserved records that the last layout overflowed the viewport and one
	// column was handed to the scrollbar. Starting the next layout at the
	// reserved width keeps the stored prefix wrap valid across reflows
	// (probing full width first would re-render the whole window at the other
	// width on each pass).
	barReserved bool

	// lines caches the last full content build (committed prefix + live tail):
	// the transcript's full content lines in order. Mouse selection endpoints
	// anchor into these indices (see selection), so scrolling preserves the
	// highlight instead of clearing it; streaming only appends, keeping earlier
	// indices stable. Materializing deferred history prepends lines, and the
	// model shifts a live selection by the count takePrepend reports.
	lines []string

	// cardSpans maps each tool card to its [first,last] content-line range in
	// the last renderContent, so a mouse click can be routed to the card under the
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

	// prepended accumulates content lines inserted above the previous top by
	// the lazy seed since the last takePrepend, so the model can shift a live
	// selection's anchors (see takePrepend).
	prepended int
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
		prefixValid:     true,
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
	*t = transcript{vp: vp, theme: theme, totalWidth: totalWidth, width: width, activeAssistant: -1, prefixValid: true, follow: true}
}

// toggleCardExpanded flips a tool card's expand state and, when the card is
// already part of the committed prefix, drops the stored wrap so the next
// layout repaints it. A card can be clicked open long after its block was
// folded into the prefix, and commit tracking cannot see the mutation.
func (t *transcript) toggleCardExpanded(c *toolCard) {
	c.toggleExpanded()
	for i := t.matStart; i < t.matEnd; i++ {
		if t.blocks[i].card == c {
			t.prefixValid = false
			return
		}
	}
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

// setBannerText replaces the startup splash's pre-rendered text in place and
// invalidates the stored prefix so the next reflow repaints it. The model uses
// this to advance the animated wordmark; keeping the block at the same index
// preserves every later block's offset and the scroll/selection anchors that
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
		// The banner sits at the top of the transcript. Drop the stored wrap so
		// the next layout repaints it — but only when it is actually inside the
		// materialized window: a deferred banner contributes no prefix lines.
		if i >= t.matStart && i < t.matEnd {
			t.prefixValid = false
		}
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

// addReviewNote attaches a permission-gate verdict to the tool card it decided.
// The card is announced while the call streams — before the gate runs — so the
// note arrives later and is folded into that card, which renders it directly
// below the command and above the output (see toolCard.renderNotes): the
// verdict stays next to the command no matter how long the output or diff
// grows. A note whose card is unknown (a task child's gate decision, or a
// card-less replay edge) is filed as a standalone block at the end.
func (t *transcript) addReviewNote(n judge.Note) {
	if n.ToolCallID != "" {
		for i := range t.blocks {
			b := t.blocks[i]
			if b.role == roleTool && b.card != nil && b.card.id == n.ToolCallID {
				b.card.addNote(n)
				// The card may already sit inside the committed prefix, whose
				// stored wrap predates the note; drop it so the next layout
				// repaints the card with the verdict. Cards outside the window
				// need no handling: deferred blocks render on first
				// materialization, and tail blocks render fresh each layout.
				for j := t.matStart; j < t.matEnd; j++ {
					if t.blocks[j].card == b.card {
						t.prefixValid = false
						break
					}
				}
				t.reflow()
				return
			}
		}
	}
	t.blocks = append(t.blocks, transcriptBlock{role: roleReview, note: &n})
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
	t.extendSeed()
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
	t.extendSeed()
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
	t.extendSeed()
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
// the stored prefix wrap survives from one reflow to the next instead of being
// invalidated by a full-width probe on every pass. The content only re-probes
// the full width when it fits again at totalWidth-1 (session switch, cleared
// transcript).
func (t *transcript) reflow() {
	if t.batchDepth > 0 {
		return
	}
	// Fold everything stable into the prefix first: the layout below then only
	// re-renders the live tail on top of one reused string.
	t.commitStable()
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

// Deferred-history (lazy seed) tuning. The trigger is deliberately far above
// anything a live run produces between two commits — a turn commits a handful
// of blocks, one card at a time — so only a replay, a resumed or imported
// session landing its whole history in one batch, turns lazy seeding on.
const (
	lazySeedTrigger   = 128 // stable blocks in one commit pass before deferring
	lazySeedWindow    = 64  // newest blocks rendered up front / per scroll chunk
	lazySeedMaxChunks = 16  // chunks one extendSeed call materializes at most
)

// commitStable folds every newly stable block into the prefix. The scan walks
// from the current boundary and stops at the first still-active block (the
// streaming assistant block or a running card), so an active block also pins
// everything after it out of the prefix.
func (t *transcript) commitStable() {
	k := t.matEnd
	for k < len(t.blocks) && stableBlock(t.blocks[k], k == t.activeAssistant) {
		k++
	}
	if k == t.matEnd {
		return
	}
	if k-t.matEnd > lazySeedTrigger {
		// A replay landed in one pass: keep only its newest window and defer
		// the rest. The scan just proved [matEnd, k) stable, so deferring part
		// of it is safe; a previously materialized window (if any) is dropped
		// too and re-materialized from its source blocks when the user scrolls
		// back up.
		t.matStart = k - lazySeedWindow
		t.matEnd = t.matStart
		t.prefixLines = t.prefixLines[:0]
		t.prefixCounts = t.prefixCounts[:0]
		t.prefixValid = true
	}
	if !t.prefixValid || t.prefixWidth != t.width {
		t.rebuildPrefix()
	}
	for i := t.matEnd; i < k; i++ {
		before := len(t.prefixLines)
		t.prefixLines = t.appendBlockLines(t.prefixLines, i)
		t.prefixCounts = append(t.prefixCounts, len(t.prefixLines)-before)
	}
	t.matEnd = k
}

// rebuildPrefix re-renders the materialized window into prefixLines at the
// current content width. It runs on a width change (resize, scrollbar column
// handoff) and on in-place edits inside the committed region.
func (t *transcript) rebuildPrefix() {
	t.prefixLines = t.prefixLines[:0]
	t.prefixCounts = t.prefixCounts[:0]
	for i := t.matStart; i < t.matEnd; i++ {
		before := len(t.prefixLines)
		t.prefixLines = t.appendBlockLines(t.prefixLines, i)
		t.prefixCounts = append(t.prefixCounts, len(t.prefixLines)-before)
	}
	t.prefixValid = true
	t.prefixWidth = t.width
}

// extendSeed materializes deferred history when the viewport sits near the top
// of the materialized window: the next chunk of older blocks is rendered,
// prepended, and the offset is slid down by the same amount so the content
// under the cursor does not move. One call materializes enough to leave a
// screen of fresh history above the user's position (usually a single chunk),
// so scrolling up through a long resumed session stays smooth because each
// step is bounded work.
func (t *transcript) extendSeed() {
	if t.batchDepth > 0 || t.matStart == 0 || t.width <= 0 {
		return
	}
	if t.vp.Height() <= 0 {
		return
	}
	// Pinned to a scrollable bottom: the user is watching the live tail, not
	// history. A pinned transcript that does not overflow is the exception —
	// its top and bottom are the same place, and materializing is the only way
	// to reveal that there is more above.
	if t.follow && t.overflowing() {
		return
	}
	base := t.vp.YOffset()
	margin := t.vp.Height()
	added := 0
	for i := 0; i < lazySeedMaxChunks && t.matStart > 0; i++ {
		if base+added > margin {
			break
		}
		n := t.materializeChunk()
		if n == 0 {
			break
		}
		added += n
	}
	if added == 0 {
		return
	}
	t.reflow()
	// Everything new landed above the previous first line, so sliding the
	// offset down by exactly that many lines keeps the view on the same text.
	t.vp.SetYOffset(base + added)
	// Growth above the fold is not "new output": undo the unseen count
	// setContent just attributed to it.
	t.unseen -= added
	if t.unseen < 0 {
		t.unseen = 0
	}
	t.prepended += added
}

// materializeChunk renders the next deferred chunk of blocks above the
// materialized window, prepends its lines (and per-block line counts) to the
// prefix, and returns the number of lines added.
func (t *transcript) materializeChunk() int {
	if t.matStart == 0 {
		return 0
	}
	if !t.prefixValid || t.prefixWidth != t.width {
		t.rebuildPrefix()
	}
	from := t.matStart - lazySeedWindow
	if from < 0 {
		from = 0
	}
	var lines []string
	var counts []int
	for i := from; i < t.matStart; i++ {
		before := len(lines)
		lines = t.appendBlockLines(lines, i)
		counts = append(counts, len(lines)-before)
	}
	t.prefixLines = append(lines, t.prefixLines...)
	t.prefixCounts = append(counts, t.prefixCounts...)
	t.matStart = from
	return len(lines)
}

// takePrepend returns and clears the number of content lines the lazy seed
// inserted above the previous top since the last call. The model uses it to
// shift a live selection's content-line anchors so the highlight keeps pointing
// at the same text.
func (t *transcript) takePrepend() int {
	n := t.prepended
	t.prepended = 0
	return n
}

// layoutAt lays the prefix + live tail out at width w and pushes the render
// into the viewport. A width change invalidates the stored prefix wrap, so the
// window is re-rendered at the new width on the next content build.
func (t *transcript) layoutAt(w int) {
	if w != t.width {
		t.prefixValid = false
	}
	t.width = w
	t.vp.SetWidth(w)
	t.setContent()
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

// setContent renders the full content (committed prefix + live tail) into the
// viewport and caches its lines for content-anchored mouse selection (see
// lines).
func (t *transcript) setContent() {
	before := t.vp.TotalLineCount()
	t.lines = t.renderContent()
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

// renderContent builds the transcript's full content lines: the committed
// prefix followed by the live tail, which alone is re-rendered every pass.
// Committed blocks never re-render on the streaming path — a delta only pays
// for the streaming assistant block and any running cards. cardSpans is rebuilt
// in the same walk so a click routes to the card under the cursor, committed or
// live.
func (t *transcript) renderContent() []string {
	if !t.prefixValid || t.prefixWidth != t.width {
		t.rebuildPrefix()
	}
	if t.cardSpans == nil {
		t.cardSpans = map[*toolCard][2]int{}
	} else {
		clear(t.cardSpans)
	}
	lines := append(t.lines[:0], t.prefixLines...)
	cursor := 0
	for j, count := range t.prefixCounts {
		i := t.matStart + j
		if blk := t.blocks[i]; blk.role == roleTool && blk.card != nil {
			start := cursor
			if blockLeadBlank(t.blocks, i) {
				start++
			}
			t.cardSpans[blk.card] = [2]int{start, cursor + count - 1}
		}
		cursor += count
	}
	for i := t.matEnd; i < len(t.blocks); i++ {
		start := cursor
		if blockLeadBlank(t.blocks, i) {
			start++
		}
		lines = t.appendBlockLines(lines, i)
		cursor = len(lines)
		if blk := t.blocks[i]; blk.role == roleTool && blk.card != nil {
			t.cardSpans[blk.card] = [2]int{start, cursor - 1}
		}
	}
	t.lines = lines
	return lines
}

// appendBlockLines appends block i's rendered contribution to dst: the block's
// own lines plus, when the user-adjacency rule calls for it, a leading blank
// line separating it from the previous block. This reproduces exactly the lines
// the old full-string join produced for the block, so a prefix built
// incrementally and a tail rendered per frame concatenate into the same
// transcript. User turns keep a blank line on both sides so the bar breathes
// like codex's history cell.
func (t *transcript) appendBlockLines(dst []string, i int) []string {
	if blockLeadBlank(t.blocks, i) {
		dst = append(dst, "")
	}
	return append(dst, strings.Split(t.renderBlock(t.blocks[i], i == t.activeAssistant), "\n")...)
}

// blockLeadBlank reports whether block i renders with a leading blank
// separator: the user-adjacency rule that gives a user bar room on both sides.
// Card hit-testing skips this line so a click maps to the card's own rows.
func blockLeadBlank(blocks []transcriptBlock, i int) bool {
	return i > 0 && (blocks[i].role == roleUser || blocks[i-1].role == roleUser)
}

// toolCardAt returns the tool card whose rendered block covers content line
// index line, or nil when that line belongs to another block. It reads the
// spans recorded by the last renderContent, so it is only meaningful for clicks
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
// finished tool card changes only when the user expands it (toggleCardExpanded
// invalidates the committed prefix). The streaming assistant block and running
// cards must re-render every pass.
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

// renderReviewBlock paints one standalone verdict block (a note whose tool
// card could not be found). Card-attached notes render through
// toolCard.renderNotes instead; both share renderNoteLine.
func (t *transcript) renderReviewBlock(blk transcriptBlock) string {
	if blk.note == nil {
		return ""
	}
	return renderNoteLine(t.theme, t.width, *blk.note)
}

// renderNoteLine paints one gate verdict: a warn color for approvals and
// containment (the call proceeds), an error color for denials and blocks. The
// line wraps to the transcript width; the icon leads.
func renderNoteLine(theme Theme, width int, n judge.Note) string {
	style := theme.Warn
	if n.Kind == judge.NoteDenied || n.Kind == judge.NoteBlockedNoPrompt || n.Kind == judge.NoteReadOnly {
		style = theme.Error
	}
	return style.Render(WrapToWidth(judge.FormatNote(n), width))
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
