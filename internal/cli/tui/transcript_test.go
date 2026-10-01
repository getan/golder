package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli/ui"
)

// ansiRE strips SGR escape sequences so tests can inspect the raw text the
// transcript stored, independent of the theme's coloring.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// apply runs one Update tick and returns the concrete Model, failing on an
// unexpected model type. It keeps the streaming tests terse.
func apply(t *testing.T, m tea.Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return got
}

// TestTranscriptStreamingConcat feeds a run of text deltas then a turn end and
// asserts the assistant block accumulates the deltas in order and the joined
// text is rendered in the View.
func TestTranscriptStreamingConcat(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, textDeltaMsg{delta: "Hello "})
	m = apply(t, m, textDeltaMsg{delta: "world"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("Hello world")},
	}})

	if n := len(m.transcript.blocks); n != 1 {
		t.Fatalf("block count = %d, want 1 assistant block", n)
	}
	if got := m.transcript.blocks[0]; got.role != roleAssistant || got.text != "Hello world" {
		t.Errorf("assistant block = %+v, want role assistant text %q", got, "Hello world")
	}
	if content := stripANSI(m.View().Content); !strings.Contains(content, "Hello world") {
		t.Errorf("rendered View missing streamed text; got:\n%s", content)
	}
	// The turn was finalized, so a fresh delta starts a NEW assistant block.
	if m.transcript.activeAssistant != -1 {
		t.Errorf("activeAssistant = %d after turn end, want -1", m.transcript.activeAssistant)
	}
}

// TestTranscriptSurfacesTurnError verifies a turn that ends with stopReason
// error surfaces the provider's error message as a system block rather than
// finalizing an empty turn and returning silently to the prompt. The loop
// delivers request failures (e.g. a 4xx) this way — as a terminal assistant
// message via TurnEndEvent, not as the run's result error — so without the
// StopReason check in the turnEndMsg handler the TUI would show nothing at all.
func TestTranscriptSurfacesTurnError(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		StopReason:   agentcore.StopReasonError,
		ErrorMessage: "upstream 401: 无效的令牌",
	}})

	var sys string
	for _, b := range m.transcript.blocks {
		if b.role == roleSystem {
			sys = b.text
		}
	}
	if !strings.Contains(sys, "error:") || !strings.Contains(sys, "upstream 401: 无效的令牌") {
		t.Errorf("turn error not surfaced; system block = %q", sys)
	}
	if content := stripANSI(m.View().Content); !strings.Contains(content, "upstream 401") {
		t.Errorf("rendered View missing the surfaced error; got:\n%s", content)
	}
}

// TestTranscriptSurfacesAbortedTurn verifies a turn that ends with stopReason
// aborted is flagged rather than returning silently.
func TestTranscriptSurfacesAbortedTurn(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		StopReason: agentcore.StopReasonAborted,
	}})

	var sys string
	for _, b := range m.transcript.blocks {
		if b.role == roleSystem {
			sys = b.text
		}
	}
	if !strings.Contains(sys, "aborted") {
		t.Errorf("aborted turn not surfaced; system block = %q", sys)
	}
}

// TestTranscriptNotesEmptyResponse verifies a clean end_turn that produced no
// content and no tool results is flagged with a note (with a provider-mismatch
// hint) instead of returning silently to the prompt — the shape produced when an
// endpoint accepts the request with a 200 but returns nothing decodable.
func TestTranscriptNotesEmptyResponse(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		StopReason: agentcore.StopReasonEndTurn,
	}})

	var sys string
	for _, b := range m.transcript.blocks {
		if b.role == roleSystem {
			sys = b.text
		}
	}
	if !strings.Contains(sys, "empty response from the model") {
		t.Errorf("empty response not flagged; system block = %q", sys)
	}
}

// TestTranscriptCleanTurnNoNote verifies a normal turn with content does NOT add
// a spurious error/empty system note.
func TestTranscriptCleanTurnNoNote(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		StopReason: agentcore.StopReasonEndTurn,
		Content:    agentcore.ContentList{agentcore.NewTextContent("the answer")},
	}})

	for _, b := range m.transcript.blocks {
		if b.role == roleSystem {
			t.Errorf("clean turn should add no system note, got %q", b.text)
		}
	}
}

// TestTranscriptAutoStick verifies the stick-to-bottom rule: while the viewport
// is at the bottom, new content keeps it pinned there; once the user scrolls up,
// streamed content no longer forces a jump to the bottom — but submitting a new
// turn (addUser) re-arms follow and snaps back to the newest output.
func TestTranscriptAutoStick(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 3) // 3 visible rows

	for i := 0; i < 6; i++ {
		tr.addUser("line")
	}
	if !tr.vp.AtBottom() {
		t.Fatal("transcript should stick to the bottom while at the bottom")
	}

	// More content while pinned keeps it pinned.
	tr.addUser("more")
	if !tr.vp.AtBottom() {
		t.Fatal("new content should keep a bottom-pinned transcript at the bottom")
	}

	// Simulate a user scroll-up through the viewport's key handling.
	tr.update(tea.KeyPressMsg{Code: tea.KeyUp})
	if tr.vp.AtBottom() {
		t.Fatal("scrolling up should move the viewport off the bottom")
	}

	// Streamed content arriving while scrolled up must NOT yank the view back to
	// the bottom — the user is reading history.
	tr.appendDelta("streamed while reading history\nsecond line\nthird line")
	if tr.vp.AtBottom() {
		t.Error("auto-stick should stay paused after the user scrolls up")
	}

	// Submitting a new turn is an explicit action: it re-arms follow and snaps
	// back to the newest output so the reply is never left off-screen.
	tr.addUser("a brand new prompt")
	if !tr.vp.AtBottom() {
		t.Error("submitting a new turn should re-arm auto-scroll to the bottom")
	}
}

// TestTranscriptCJKWrap feeds a long CJK line into a narrow transcript and
// asserts every wrapped line fits the width in display columns (not bytes) and
// that no rune was dropped or split.
func TestTranscriptCJKWrap(t *testing.T) {
	const width = 10
	tr := newTranscript(DefaultTheme())
	tr.setSize(width, 20)

	line := strings.Repeat("你好世界", 5) // 20 CJK runes = 40 display columns
	tr.addUser(line)

	content := tr.vp.GetContent()
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the CJK line to wrap onto multiple rows, got %d line(s)", len(lines))
	}
	for i, ln := range lines {
		if w := ui.Width(ln); w > width {
			t.Errorf("wrapped line %d width = %d columns, want <= %d: %q", i, w, width, stripANSI(ln))
		}
	}
	// No rune was cut or dropped: every source rune survives the wrap.
	if got := strings.Count(stripANSI(content), "你"); got != 5 {
		t.Errorf("counted %d 你 runes after wrap, want 5", got)
	}
}

// TestTranscriptScrollbar verifies the scrollbar policy: the gutter is hidden
// while the content fits (nothing to scroll) and appears only once the content
// overflows. When overflowing, a rounded pill thumb (body "█" with half-block
// caps "▄"/"▀") sits alongside the thin groove "│".
func TestTranscriptScrollbar(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 8) // user bars carry 1-line vertical padding, so fit needs room

	// Two short bars fit in 8 rows: no scrollbar at all — no thumb, no groove.
	tr.addUser("one")
	tr.addUser("two")
	if tr.overflowing() {
		t.Fatal("transcript should not overflow while content fits")
	}
	fit := stripANSI(tr.view())
	if strings.ContainsAny(fit, "█▄▀│") {
		t.Errorf("expected no scrollbar glyphs while content fits; got:\n%q", fit)
	}

	// Enough lines to exceed 8 rows: now it overflows, thumb shrinks and the
	// groove appears.
	for i := 0; i < 10; i++ {
		tr.addUser("line")
	}
	if !tr.overflowing() {
		t.Fatal("transcript should overflow once content exceeds the viewport")
	}
	view := stripANSI(tr.view())
	if !strings.Contains(view, "▄") || !strings.Contains(view, "▀") {
		t.Errorf("expected a rounded pill thumb (▄ top, ▀ bottom) while overflowing; got:\n%q", view)
	}
	if !strings.Contains(view, "│") {
		t.Errorf("expected a groove │ while overflowing; got:\n%q", view)
	}
	if strings.Contains(view, "░") {
		t.Errorf("scrollbar no longer uses the shaded track ░; got:\n%q", view)
	}
}

// TestTranscriptScrollToRow checks the click/drag mapping: pressing the top of
// the gutter scrolls to the top, the bottom scrolls to the bottom, and it is a
// no-op when the content fits.
func TestTranscriptScrollToRow(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 4)

	// Content fits: dragging must not move a non-scrollable viewport.
	tr.addUser("only line")
	tr.scrollToRow(3)
	if tr.vp.YOffset() != 0 {
		t.Errorf("scrollToRow on non-overflowing viewport moved offset to %d, want 0", tr.vp.YOffset())
	}

	for i := 0; i < 20; i++ {
		tr.addUser("line")
	}
	if !tr.overflowing() {
		t.Fatal("expected overflow after filling the transcript")
	}

	tr.scrollToRow(0)
	if !tr.vp.AtTop() {
		t.Errorf("dragging to row 0 should scroll to the top; YOffset=%d", tr.vp.YOffset())
	}

	tr.scrollToRow(tr.viewportHeight() - 1)
	if !tr.vp.AtBottom() {
		t.Errorf("dragging to the last row should scroll to the bottom; YOffset=%d", tr.vp.YOffset())
	}
}

// TestModelScrollbarDrag drives the model with mouse press/motion/release on the
// scrollbar column and asserts the drag state toggles and the viewport scrolls.
func TestModelScrollbarDrag(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 30, Height: 8})
	for i := 0; i < 40; i++ {
		m.transcript.addUser("line")
	}
	if !m.transcript.overflowing() {
		t.Fatal("expected the transcript to overflow")
	}

	col := m.width - 1
	// Press at the top of the gutter: drag begins and the view jumps to the top.
	m = apply(t, m, tea.MouseClickMsg{X: col, Y: 0, Button: tea.MouseLeft})
	if !m.draggingScrollbar {
		t.Fatal("left press on the scrollbar column should start dragging")
	}
	if !m.transcript.vp.AtTop() {
		t.Errorf("press at row 0 should scroll to top; YOffset=%d", m.transcript.vp.YOffset())
	}

	// Motion to the bottom row while held drags the thumb down.
	m = apply(t, m, tea.MouseMotionMsg{X: col, Y: m.transcript.viewportHeight() - 1, Button: tea.MouseLeft})
	if !m.transcript.vp.AtBottom() {
		t.Errorf("motion to the last row while dragging should scroll to bottom; YOffset=%d", m.transcript.vp.YOffset())
	}

	// Release ends the drag.
	m = apply(t, m, tea.MouseReleaseMsg{X: col, Y: 3, Button: tea.MouseLeft})
	if m.draggingScrollbar {
		t.Error("release should end the scrollbar drag")
	}

	// A press away from the gutter column must not start a drag.
	m = apply(t, m, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if m.draggingScrollbar {
		t.Error("press off the scrollbar column should not start dragging")
	}
}

// TestTranscriptUserGutter verifies user turns render as a codex-style bar: a
// single leading › gutter plus full-width background so prompts never blend
// into assistant replies. Continuation lines indent instead of repeating ›.
func TestTranscriptUserGutter(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 20)
	tr.addUser("hello world")
	content := stripANSI(tr.vp.GetContent())
	if !strings.Contains(content, "› hello world") {
		t.Errorf("user block should carry › gutter; got:\n%q", content)
	}
	tr.addUser("line one\nline two")
	content = stripANSI(tr.vp.GetContent())
	if !strings.Contains(content, "› line one") {
		t.Errorf("multiline user block should gutter first line; got:\n%q", content)
	}
	if strings.Contains(content, "› line two") {
		t.Errorf("continuation lines should indent, not repeat ›; got:\n%q", content)
	}
	if !strings.Contains(content, "  line two") {
		t.Errorf("continuation line should indent two cells; got:\n%q", content)
	}
}

// TestStreamingMarkdownNoFlash streams an answer in pieces and asserts the last
// streaming render is byte-identical to the finalized render: both phases run
// the same renderMarkdown, so turn-end is a no-op instead of a plain→styled
// flash (codex parity: style incrementally, never restyle at the end).
func TestStreamingMarkdownNoFlash(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	full := "Go 的 `select` 专门等多个 channel：\n\n- 多路复用\n- 随机选一个\n"
	// Split mid-construct on purpose: partial Markdown must still render.
	tr.appendDelta(full[:20])
	_ = tr.renderAll()
	tr.appendDelta(full[20:])
	streaming := tr.renderAll()

	tr.finalizeTurn(agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent(full)},
	})
	if got := tr.renderAll(); got != streaming {
		t.Errorf("finalized render differs from streaming render (flash):\nstreaming: %q\nfinalized: %q", streaming, got)
	}
}

// TestStableBlockCacheReuse finalizes one assistant turn, streams the next, and
// asserts the sealed block's render is memoized (not re-run per delta) and
// survives width-preserving reflows but drops on resize.
func TestStableBlockCacheReuse(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	tr.appendDelta("first turn")
	tr.finalizeTurn(agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("first turn")},
	})
	first := tr.renderAll()
	sealed, ok := tr.renderCache[0]
	if !ok {
		t.Fatal("sealed assistant block must be cached after render")
	}
	tr.appendDelta("second")
	second := tr.renderAll()
	if tr.renderCache[0] != sealed {
		t.Error("streaming the next turn must not evict the sealed block's cache")
	}
	if !strings.HasPrefix(second, first) {
		t.Errorf("second render must extend the first render:\nfirst: %q\nsecond: %q", first, second)
	}
	tr.setSize(50, 12)
	_ = tr.renderAll()
	if _, ok := tr.renderCache[0]; !ok {
		t.Fatal("resize must rebuild the cache entry at the new width")
	}
}

// TestFinalizeTurnDoesNotDuplicateSealedNarration is the regression for the
// "same narration above and below every command" bug: when a live tool
// announcement seals the streamed narration before its card and nothing
// streams after the card, the turn-end finalize must not re-append the full
// message text below the card.
func TestFinalizeTurnDoesNotDuplicateSealedNarration(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	const narration = "正在检查仓库状态，准备提交推送。"
	tr.appendDelta(narration)
	tr.announceToolCard(&toolCard{name: "bash", state: cardSuccess})
	tr.finalizeTurn(agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewTextContent(narration),
			agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: "1", Name: "bash"},
		},
	})

	assistantBlocks := 0
	for _, blk := range tr.blocks {
		if blk.role == roleAssistant && strings.Contains(blk.text, narration) {
			assistantBlocks++
		}
	}
	if assistantBlocks != 1 {
		t.Fatalf("narration appears in %d assistant blocks, want 1\n%+v", assistantBlocks, tr.blocks)
	}
	if len(tr.blocks) != 2 || tr.blocks[0].role != roleAssistant || tr.blocks[1].role != roleTool {
		t.Fatalf("block order = %+v, want [assistant tool]", tr.blocks)
	}
}

// TestFinalizeTurnKeepsPostCardDeltas guards the neighboring case: deltas
// after the sealed card form their own block, and the full message text must
// not overwrite that block with the pre-card narration.
func TestFinalizeTurnKeepsPostCardDeltas(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.appendDelta("before. ")
	tr.announceToolCard(&toolCard{name: "bash", state: cardSuccess})
	tr.appendDelta("after.")
	tr.finalizeTurn(agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewTextContent("before. after."),
			agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: "1", Name: "bash"},
		},
	})
	if len(tr.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3 (before, card, after)\n%+v", len(tr.blocks), tr.blocks)
	}
	if tr.blocks[0].text != "before. " || tr.blocks[2].text != "after." {
		t.Fatalf("texts = %q / %q, want deltas kept on both sides of the card", tr.blocks[0].text, tr.blocks[2].text)
	}
}
