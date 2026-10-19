package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/runtime"
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
// aborted is flagged rather than returning silently, using the same notice the
// interrupt press settles on (ui.InterruptNotice) so one user action reads the
// same in both front-ends.
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
	if !strings.Contains(sys, ui.InterruptNotice) {
		t.Errorf("aborted turn not surfaced with %q; system block = %q", ui.InterruptNotice, sys)
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

// TestTranscriptNotesReasoningOnlyTurn covers the shape that used to settle in
// silence: the model streamed reasoning, ended the turn, and produced no answer
// and no tool call. The guard used to ask "is there any content?", and a
// reasoning block is content — so a stalled run looked finished and the
// transcript simply stopped after the last tool card, with no way for the person
// to tell the two apart. Now the turn says which shape it was.
func TestTranscriptNotesReasoningOnlyTurn(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		StopReason: agentcore.StopReasonEndTurn,
		Content:    agentcore.ContentList{agentcore.NewThinkingContent("one more file to read before I answer")},
	}})

	var sys string
	for _, b := range m.transcript.blocks {
		if b.role == roleSystem {
			sys = b.text
		}
	}
	if !strings.Contains(sys, "reasoning only") {
		t.Errorf("reasoning-only turn not flagged; system block = %q", sys)
	}
	if strings.Contains(sys, "Check that --model") {
		t.Errorf("reasoning-only turn got the protocol-mismatch hint, which is the other shape: %q", sys)
	}
}

// TestTranscriptNotesStall: when the loop carries on by itself after a
// reasoning-only turn, the transcript says so. Without it the person reads
// "send another message" while the run is already continuing — and, when the
// budget is spent, has no way to tell that the run stopped there.
func TestTranscriptNotesStall(t *testing.T) {
	turn := func(stall runtime.TurnStall) turnEndMsg {
		return turnEndMsg{
			msg: agentcore.AssistantMessage{
				StopReason: agentcore.StopReasonEndTurn,
				Content:    agentcore.ContentList{agentcore.NewThinkingContent("still thinking about it")},
			},
			stall: stall,
		}
	}
	lastSystem := func(m Model) string {
		var sys string
		for _, b := range m.transcript.blocks {
			if b.role == roleSystem {
				sys = b.text
			}
		}
		return sys
	}

	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, turn(runtime.TurnStall{Count: 1, Limit: 2}))
	if sys := lastSystem(m); !strings.Contains(sys, "carrying on automatically (attempt 1 of 2)") {
		t.Errorf("stall note = %q, want the automatic continuation named", sys)
	} else if strings.Contains(sys, "Send another message") {
		t.Errorf("stall note = %q, want no instruction to do what the loop is already doing", sys)
	}

	m = apply(t, m, turn(runtime.TurnStall{Count: 3, Limit: 2}))
	if sys := lastSystem(m); !strings.Contains(sys, "stopping here") {
		t.Errorf("exhausted note = %q, want it to say the run stopped", sys)
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

	if tr.unseen == 0 {
		t.Error("content arriving while paused should accumulate the unseen counter")
	}

	// End (jumpToBottom) is the explicit way back: it pins the viewport to the
	// newest content and clears the "N new lines" hint in one step.
	tr.jumpToBottom()
	if !tr.vp.AtBottom() {
		t.Error("jumpToBottom should pin the viewport to the newest content")
	}
	if tr.unseen != 0 {
		t.Errorf("jumpToBottom should clear the unseen counter, got %d", tr.unseen)
	}

	// Submitting a new turn is an explicit action: it re-arms follow and snaps
	// back to the newest output so the reply is never left off-screen.
	tr.update(tea.KeyPressMsg{Code: tea.KeyUp})
	if tr.vp.AtBottom() {
		t.Fatal("scrolling up again should move off the bottom")
	}
	tr.addUser("a brand new prompt")
	if !tr.vp.AtBottom() {
		t.Error("submitting a new turn should re-arm auto-scroll to the bottom")
	}
}

// TestTranscriptUnseenClearsOnScrollBack verifies the other half of the
// contract: the unseen counter is not End-only — manually scrolling back to the
// bottom re-arms follow and drops the hint, so no stale "new lines" notice is
// left behind.
func TestTranscriptUnseenClearsOnScrollBack(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 3)
	for i := 0; i < 6; i++ {
		tr.addUser("line")
	}
	tr.update(tea.KeyPressMsg{Code: tea.KeyUp})
	tr.appendDelta("one\ntwo")
	if tr.unseen == 0 {
		t.Fatal("streamed lines should count as unseen while scrolled up")
	}
	for i := 0; i < 50 && !tr.vp.AtBottom(); i++ {
		tr.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if !tr.vp.AtBottom() {
		t.Fatal("scrolling back down should reach the bottom")
	}
	if tr.unseen != 0 {
		t.Errorf("reaching the bottom should clear unseen, got %d", tr.unseen)
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
	tr.reflow()
	tr.appendDelta(full[20:])
	streaming := strings.Join(tr.contentLines(), "\n")

	tr.finalizeTurn(agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent(full)},
	})
	if got := strings.Join(tr.contentLines(), "\n"); got != streaming {
		t.Errorf("finalized render differs from streaming render (flash):\nstreaming: %q\nfinalized: %q", streaming, got)
	}
}

// TestStablePrefixReuse finalizes one assistant turn, streams the next, and
// asserts the sealed block is folded into the committed prefix and reused
// verbatim across streaming reflows — a delta must not re-run glamour (or a
// finished card's diff) over history. A width change re-renders the window.
func TestStablePrefixReuse(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	tr.appendDelta("first turn")
	tr.finalizeTurn(agentcore.AssistantMessage{
		Content: agentcore.ContentList{agentcore.NewTextContent("first turn")},
	})
	if tr.matEnd != 1 {
		t.Fatalf("matEnd = %d, want the sealed block folded into the prefix", tr.matEnd)
	}
	if len(tr.prefixLines) == 0 {
		t.Fatal("sealed block must contribute prefix lines")
	}
	// Poison the committed wrap: a streaming reflow that re-rendered the
	// sealed block would wipe the sentinel; reuse keeps it.
	tr.prefixLines[0] = "CACHED-SENTINEL"
	tr.appendDelta("second")
	body := strings.Join(tr.contentLines(), "\n")
	if !strings.Contains(body, "CACHED-SENTINEL") {
		t.Error("streaming reflow re-rendered the committed prefix instead of reusing it")
	}
	if !strings.Contains(body, "second") {
		t.Errorf("streaming tail missing from render:\n%s", body)
	}

	// A width change invalidates the stored wrap and re-renders the window.
	tr.setSize(50, 12)
	body = strings.Join(tr.contentLines(), "\n")
	if strings.Contains(body, "CACHED-SENTINEL") {
		t.Error("resize must drop the stale prefix wrap")
	}
	if !strings.Contains(body, "first turn") {
		t.Errorf("rebuilt prefix lost the sealed block:\n%s", body)
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

// TestTranscriptLocksHorizontalScroll is the regression for the "trackpad
// swipe slides the transcript sideways and hides the card bullets" report:
// the transcript lays content out to the exact width itself, so horizontal
// scrolling must be a no-op (codex locks the axis). The banner is emitted
// verbatim, so an over-wide banner is the realistic trigger that gives the
// viewport a non-zero maxXOffset.
func TestTranscriptLocksHorizontalScroll(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 10)
	tr.addBanner(strings.Repeat("=", 120)) // over-wide, unwrapped block
	tr.addSystem("short line")

	if tr.vp.XOffset() != 0 {
		t.Fatalf("initial XOffset = %d, want 0", tr.vp.XOffset())
	}
	for _, msg := range []tea.Msg{
		tea.MouseWheelMsg{Button: tea.MouseWheelLeft},
		tea.MouseWheelMsg{Button: tea.MouseWheelRight},
		tea.MouseWheelMsg{Button: tea.MouseWheelUp},   // plain vertical wheel
		tea.MouseWheelMsg{Button: tea.MouseWheelDown}, // plain vertical wheel
	} {
		tr.update(msg)
	}
	if got := tr.vp.XOffset(); got != 0 {
		t.Fatalf("XOffset = %d after wheel events, want locked at 0", got)
	}

	// Shift+wheel is the viewport's other horizontal path.
	shift := tea.MouseWheelMsg{Button: tea.MouseWheelUp, Mod: tea.ModShift}
	tr.update(shift)
	if got := tr.vp.XOffset(); got != 0 {
		t.Fatalf("XOffset = %d after shift+wheel, want locked at 0", got)
	}
}

// TestFinishedCardCommittedAndToggleInvalidates guards the streaming freeze and
// the late expand: a finished tool card is folded into the committed prefix
// (an expanded patch card renders a full colored diff, ~80ms on a real
// session, and must not re-render per delta), while a running card stays in the
// live tail. Expanding the card long after its block was folded must drop the
// stored wrap, because commit tracking cannot see the mutation.
func TestFinishedCardCommittedAndToggleInvalidates(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	card := &toolCard{id: "1", name: "read", input: map[string]any{"path": "internal/x.go"}, state: cardRunning}
	tr.addToolCard(card)
	if tr.matEnd != 0 {
		t.Fatal("a running card must never be folded into the prefix")
	}

	card.complete(true, "a.go\nb.go", nil)
	tr.reflow()
	if tr.matEnd != 1 {
		t.Fatalf("matEnd = %d, want the finished card folded into the prefix", tr.matEnd)
	}
	if len(tr.prefixLines) == 0 {
		t.Fatal("finished card must contribute prefix lines")
	}

	// Poison the committed wrap: a steady reflow must reuse it verbatim.
	tr.prefixLines[0] = "CACHED-SENTINEL"
	tr.reflow()
	if body := strings.Join(tr.contentLines(), "\n"); !strings.Contains(body, "CACHED-SENTINEL") {
		t.Fatal("steady-state reflow re-rendered the committed card instead of reusing the prefix")
	}

	// Expanding a committed card must invalidate the stored wrap.
	tr.toggleCardExpanded(card)
	tr.reflow()
	body := strings.Join(tr.contentLines(), "\n")
	if strings.Contains(body, "CACHED-SENTINEL") {
		t.Error("expand toggle must drop the stale prefix wrap")
	}
	if tr.matEnd != 1 {
		t.Fatalf("matEnd = %d after toggle reflow, want 1", tr.matEnd)
	}
}

// TestReflowReusesPrefixAtReservedWidth is the regression for the width churn
// that defeated block reuse: reflow probed the full width and, on overflow,
// re-laid at totalWidth-1 on every pass, so nothing ever matched the cache and
// every streaming frame re-rendered the whole history (8s per frame on a real
// session). A steady-state reflow must reuse the committed prefix verbatim.
func TestReflowReusesPrefixAtReservedWidth(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 8)
	for i := 0; i < 30; i++ {
		tr.addSystem(strings.Repeat("x", 80)) // ~3 lines each: overflows 8 rows
	}
	if !tr.barReserved {
		t.Fatal("an overflowing transcript must reserve the scrollbar column")
	}
	if tr.matEnd != 30 {
		t.Fatalf("matEnd = %d, want every stable block committed", tr.matEnd)
	}

	// Poison the committed wrap: if the next reflow reuses it (same width, no
	// invalidation), the sentinel surfaces; a re-render wipes it out.
	tr.prefixLines[0] = "CACHED-SENTINEL"
	tr.reflow()
	if body := strings.Join(tr.contentLines(), "\n"); !strings.Contains(body, "CACHED-SENTINEL") {
		t.Fatal("steady-state reflow re-rendered stable blocks instead of reusing the prefix")
	}
}

// TestLazySeedDefersHugeReplay covers the resumed-session path: a replay that
// lands more than lazySeedTrigger stable blocks in one batch renders only its
// newest window up front, so seed time and retained render memory stay
// proportional to what is looked at. Scrolling to the top materializes the
// next chunk above the viewport and slides the offset so the visible text
// does not move.
func TestLazySeedDefersHugeReplay(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	tr.beginBatch()
	const total = 300
	for i := 0; i < total; i++ {
		tr.addSystem(fmt.Sprintf("line %d", i))
	}
	tr.endBatch()

	if tr.matStart == 0 {
		t.Fatal("a replay of 300 stable blocks must defer older history")
	}
	if got := tr.matEnd - tr.matStart; got != lazySeedWindow {
		t.Fatalf("materialized window = %d blocks, want %d", got, lazySeedWindow)
	}
	body := strings.Join(tr.contentLines(), "\n")
	if strings.Contains(body, "line 235") {
		t.Error("deferred history must not be rendered into the seed")
	}
	for _, want := range []string{"line 236", "line 299"} {
		if !strings.Contains(body, want) {
			t.Errorf("seed missing %q\nstart=%d end=%d", want, tr.matStart, tr.matEnd)
		}
	}

	// Stand at the top: the next older chunk materializes — a screen of fresh
	// history, which at one line per block is Height() blocks — and the offset is
	// slid down by exactly the number of lines prepended.
	tr.vp.GotoTop()
	tr.update(nil)
	const chunk = 12 // the viewport height set above, one line per block
	if tr.matStart != total-lazySeedWindow-chunk {
		t.Fatalf("matStart = %d, want %d after one chunk", tr.matStart, total-lazySeedWindow-chunk)
	}
	added := tr.prepended
	if added == 0 {
		t.Fatal("extendSeed must report the lines it prepended")
	}
	if got := tr.vp.YOffset(); got != added {
		t.Fatalf("YOffset = %d after prepending %d lines, want the view pinned", got, added)
	}
	if got := tr.takePrepend(); got != added {
		t.Fatalf("takePrepend = %d, want %d", got, added)
	}
	if got := tr.takePrepend(); got != 0 {
		t.Fatalf("takePrepend = %d on the second call, want cleared", got)
	}
	body = strings.Join(tr.contentLines(), "\n")
	if !strings.Contains(body, "line 224") || !strings.Contains(body, "line 235") {
		t.Errorf("materialized chunk missing from content\nstart=%d", tr.matStart)
	}
}

// pinSeedBudget sets the lazy seed's render budget for one test, so a step's
// granularity is pinned: budget 0 makes every step materialize exactly one block,
// which is the worst case a slow block can produce without the test having to
// render something slow.
func pinSeedBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := lazySeedBudget
	lazySeedBudget = d
	t.Cleanup(func() { lazySeedBudget = old })
}

// seedDeferred builds a model whose transcript holds a batch replay the lazy seed
// defers, standing at the top of the materialized window (the position a scroll
// into history leaves the user in).
func seedDeferred(t *testing.T, blocks int) Model {
	t.Helper()
	m := apply(t, NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"}), tea.WindowSizeMsg{Width: 60, Height: 24})
	m.transcript.beginBatch()
	for i := 0; i < blocks; i++ {
		m.transcript.addSystem(fmt.Sprintf("line %d", i))
	}
	m.transcript.endBatch()
	if m.transcript.matStart == 0 {
		t.Fatalf("a replay of %d blocks must defer older history", blocks)
	}
	m.transcript.vp.GotoTop()
	m.transcript.follow = false
	return m
}

// TestLazySeedStepStopsAtTheBudget pins the contract that keeps a scroll event
// cheap however expensive a block is: a materialize step renders at least one
// block — so a caller looping on it always makes progress — and then stops at the
// deadline rather than walking the rest of the chunk.
func TestLazySeedStepStopsAtTheBudget(t *testing.T) {
	pinSeedBudget(t, 0)
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 12)
	tr.beginBatch()
	for i := 0; i < 300; i++ {
		tr.addSystem(fmt.Sprintf("line %d", i))
	}
	tr.endBatch()
	if tr.matStart == 0 {
		t.Fatal("a replay of 300 blocks must defer older history")
	}

	before := tr.matStart
	added := tr.materializeChunk(time.Now())
	if added == 0 {
		t.Fatal("a spent budget must still materialize one block, or a caller loops forever")
	}
	if tr.matStart != before-1 {
		t.Fatalf("matStart = %d, want %d: a spent budget must render exactly one block", tr.matStart, before-1)
	}
}

// TestSeedMoreContinuationFillsHistory covers the model half of the budgeted
// seed: arming (once, not per event), stale ticks, stepping until the fill has
// moved past the top of the materialized window, and the chain stopping itself
// there so a settled screen keeps no timer.
func TestSeedMoreContinuationFillsHistory(t *testing.T) {
	pinSeedBudget(t, 0)
	m := seedDeferred(t, 3000)

	if cmd := m.armSeedMore(); cmd == nil {
		t.Fatal("standing at the top of deferred history must arm the continuation")
	}
	if !m.seedMoreActive {
		t.Fatal("arming must mark the chain live")
	}
	if cmd := m.armSeedMore(); cmd != nil {
		t.Fatal("a live chain must not be armed twice")
	}
	if cmd := m.stepSeedMore(seedMoreMsg{gen: m.seedMoreGen - 1}); cmd != nil {
		t.Fatal("a stale tick from a superseded chain must be dropped")
	}

	start := m.transcript.matStart
	steps, blocks := 0, 0
	for {
		before := m.transcript.matStart
		cmd := m.stepSeedMore(seedMoreMsg{gen: m.seedMoreGen})
		if m.transcript.matStart < before {
			steps++
			blocks += before - m.transcript.matStart
		}
		if cmd == nil {
			break
		}
		if steps > 1000 {
			t.Fatalf("the continuation never stopped (matStart %d of %d blocks)", m.transcript.matStart, start)
		}
	}
	if steps == 0 || blocks == 0 {
		t.Fatal("the first step must materialize something")
	}
	if m.transcript.matStart >= start {
		t.Fatalf("the continuation materialized nothing: matStart %d → %d", start, m.transcript.matStart)
	}
	if m.seedMoreActive {
		t.Fatal("a stopped chain must clear its active flag")
	}
	if m.transcript.seedPending() {
		t.Fatal("the chain stopped while there was still work at the position it fills from")
	}
	t.Logf("continuation took %d steps and materialized %d blocks", steps, blocks)
}

// TestWheelScrollArmsTheSeedContinuation is the glue a flick depends on: the
// wheel path itself arms the fill, so a gesture that reaches the top of the
// materialized window keeps filling history in without another event.
func TestWheelScrollArmsTheSeedContinuation(t *testing.T) {
	pinSeedBudget(t, 0)
	m := seedDeferred(t, 3000)

	before := m.transcript.matStart
	m = apply(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.transcript.matStart >= before {
		t.Fatalf("the scroll materialized nothing: matStart %d → %d", before, m.transcript.matStart)
	}
	if m.transcript.seedPending() && !m.seedMoreActive {
		t.Fatal("a scroll that leaves history pending must arm the seed continuation")
	}
	if m.seedMoreActive && !m.transcript.seedPending() {
		t.Fatal("the continuation must not be armed with nothing to fill")
	}
}

// TestViewClosesARowThatLeavesAStyleOpen covers the guard writeRow carries: a
// row whose render leaves a style open must be closed before the padding and the
// scrollbar cell are written, or the style paints both. roleBanner is the block
// kind that can produce such a row — its text is emitted verbatim — so it is the
// one a test can build.
func TestViewClosesARowThatLeavesAStyleOpen(t *testing.T) {
	const open = "\x1b[31mopen style"
	tr := newTranscript(DefaultTheme())
	tr.setSize(20, 4)
	tr.addBanner(open)
	// Overflow, so a scrollbar cell follows every row: the reset has to land
	// before it.
	for i := 0; i < 8; i++ {
		tr.addSystem(fmt.Sprintf("line %d", i))
	}
	if !tr.overflowing() {
		t.Fatal("the fixture must overflow, or there is no bar cell to protect")
	}
	// The banner is the first block; scroll back to it so the row under test is
	// the one the window shows.
	tr.follow = false
	tr.vp.GotoTop()
	view := tr.view()
	if !strings.Contains(view, open+"\x1b[m") {
		t.Fatalf("the open style was not closed before the pad/bar:\n%q", view)
	}
	if strings.Contains(view, open+" ") {
		t.Fatalf("the style is still open across the padding:\n%q", view)
	}
}

// TestSeedShiftMovesLiveSelection pins the model glue around the lazy seed:
// when older history is materialized above the viewport, a content-anchored
// selection slides down with the text it highlights, while a below-transcript
// selection (screen rows) is left alone.
func TestSeedShiftMovesLiveSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m.sel = selection{active: true, anchor: point{2, 5}, cursor: point{2, 9}}
	m.transcript.prepended = 37
	m = apply(t, m, unseenWatchMsg{})
	if m.sel.anchor.y != 42 || m.sel.cursor.y != 46 {
		t.Fatalf("selection = %+v, want both endpoints shifted by 37", m.sel)
	}
	if m.transcript.prepended != 0 {
		t.Fatalf("prepended = %d after Update, want consumed", m.transcript.prepended)
	}

	m.sel = selection{active: true, below: true, anchor: point{2, 5}, cursor: point{2, 9}}
	m.transcript.prepended = 11
	m = apply(t, m, unseenWatchMsg{})
	if m.sel.anchor.y != 5 || m.sel.cursor.y != 9 {
		t.Fatalf("below-transcript selection moved: %+v", m.sel)
	}
}

// TestToolCardSpanSkipsUserSeparator guards card hit-testing after a user bar:
// the user-adjacency blank line belongs to neither block, so a click on it
// must not toggle the card below it.
func TestToolCardSpanSkipsUserSeparator(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 12)
	tr.addUser("hi")
	card := &toolCard{id: "1", name: "bash", state: cardSuccess}
	tr.addToolCard(card)
	span, ok := tr.cardSpans[card]
	if !ok {
		t.Fatal("card must record a hit span")
	}
	if span[0] <= 0 || span[1] >= len(tr.contentLines()) {
		t.Fatalf("span = %v outside content (%d lines)", span, len(tr.contentLines()))
	}
	if got := stripANSI(tr.contentLines()[span[0]-1]); strings.TrimSpace(got) != "" {
		t.Fatalf("card span starts on a non-blank line %d: %q", span[0]-1, got)
	}
	if got := tr.toolCardAt(span[0] - 1); got != nil {
		t.Error("the separator line must not hit the card")
	}
	if got := tr.toolCardAt(span[0]); got != card {
		t.Error("the card's first own line must hit the card")
	}
	if got := tr.toolCardAt(span[1]); got != card {
		t.Error("the card's last line must hit the card")
	}
}

// TestAppendDeltaTextDefersReflow locks the split the streaming path relies
// on: deltas only grow the live block, and the coalescing tick reflows.
func TestAppendDeltaTextDefersReflow(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(40, 8)
	tr.appendDeltaText("hello")
	if strings.Contains(strings.Join(tr.contentLines(), "\n"), "hello") {
		t.Fatal("appendDeltaText must not reflow")
	}
	tr.reflow()
	if !strings.Contains(strings.Join(tr.contentLines(), "\n"), "hello") {
		t.Fatal("reflow must render deferred deltas")
	}
}

// TestTranscriptNoticesAlignWithContentColumn pins the transcript's column: a
// marker line ("• ", "› ") starts at the margin and everything belonging to it
// continues at two columns. System notices and gate verdicts carry no marker of
// their own, so they take that same two-column indent — flush against the
// margin they were the only rows that broke the column, most visibly for an
// interrupt landing directly under the card it stopped.
func TestTranscriptNoticesAlignWithContentColumn(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addToolCard(&toolCard{
		id: "call_1", name: "bash", input: map[string]any{"command": "make test"},
		state: cardSuccess, response: []respNode{{text: "output line"}},
	})
	tr.addReviewNote(judge.Note{
		Tool: "bash", ToolCallID: "call_1", Kind: judge.NoteApproved,
		Risk: "medium", Authorization: "high", Rationale: "常规提交", Lang: "zh",
	})
	tr.addSystem(ui.InterruptingNotice)
	tr.addSystem(ui.InterruptNotice)

	raw := tr.contentLines()
	// Assert on the visible text: the marker and the notice text carry their own
	// SGR runs, so the escape-free form is what a reader (and a column check)
	// actually sees.
	lines := make([]string, len(raw))
	for i, ln := range raw {
		lines[i] = stripANSI(ln)
	}
	t.Logf("rendered:\n%s", strings.Join(lines, "\n"))

	for _, want := range []string{"• Ran bash make test", "已批准", ui.InterruptingNotice, ui.InterruptNotice} {
		found := false
		for _, ln := range lines {
			if !strings.Contains(ln, want) {
				continue
			}
			found = true
			if want == "• Ran bash make test" {
				if !strings.HasPrefix(ln, "• ") {
					t.Errorf("headline %q should start at the margin", ln)
				}
				continue
			}
			// Everything else belongs to the column: two spaces in.
			if !strings.HasPrefix(ln, contentIndent) {
				t.Errorf("line %q should start at the content column %q", ln, contentIndent)
			}
		}
		if !found {
			t.Fatalf("rendered transcript is missing %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// TestSystemNoticeWrapRespectsTheIndent verifies the indent is paid for out of
// the wrap width: a long notice wraps to the pane minus the indent, so no
// rendered line can spill past the right edge.
func TestSystemNoticeWrapRespectsTheIndent(t *testing.T) {
	const width = 40
	tr := newTranscript(DefaultTheme())
	tr.setSize(width, 20)
	tr.addSystem(strings.Repeat("word ", 30))

	for _, ln := range tr.contentLines() {
		if got := ui.Width(ln); got > width {
			t.Errorf("line width %d exceeds the pane %d: %q", got, width, ln)
		}
	}
}

// TestNoticeAndNoteShareOneLine pins the property the auto-compaction
// announcement was asked for: a session notice renders through the same function
// as a gate verdict, so it carries the same warn color and the same marker
// rather than a copy of them that can drift.
func TestNoticeAndNoteShareOneLine(t *testing.T) {
	th := DefaultTheme()
	const width = 80

	note := renderNoteLine(th, width, judge.Note{
		Tool: "bash", Kind: judge.NoteApproved, Risk: "medium", Authorization: "high",
		Rationale: "routine", Lang: "en",
	})
	notice := renderNoticeLine(th, width, "⚠ context auto-compacted: 160,000 → 24,000 tokens (12 summarized, 8 kept)", false)

	// sgrOpen is the styling a line opens with: the escape sequence a style emits
	// ahead of its content. Comparing these is how "the same color" is asserted
	// without hardcoding an escape code.
	sgrOpen := func(rendered string) string {
		t.Helper()
		if i := strings.Index(rendered, "  "); i >= 0 {
			return rendered[:i]
		}
		return rendered
	}
	warnOpen := sgrOpen(th.Warn.Render("  x"))
	errorOpen := sgrOpen(th.Error.Render("  x"))

	for name, line := range map[string]string{"note": note, "notice": notice} {
		if got := sgrOpen(line); got != warnOpen {
			t.Errorf("%s opens with %q, want the warn style %q", name, got, warnOpen)
		}
		if !strings.Contains(line, "  ⚠ ") {
			t.Errorf("%s should carry the warn marker at the content column: %q", name, line)
		}
	}

	// A failure swaps to the error style and the denial marker, for both.
	failedNote := renderNoteLine(th, width, judge.Note{Tool: "read", Kind: judge.NoteDenied, Rationale: "no", Lang: "en"})
	failedNotice := renderNoticeLine(th, width, "✗ context compaction failed: boom", true)
	for name, line := range map[string]string{"note": failedNote, "notice": failedNotice} {
		if got := sgrOpen(line); got != errorOpen {
			t.Errorf("failed %s opens with %q, want the error style %q", name, got, errorOpen)
		}
		if !strings.Contains(line, "  ✗ ") {
			t.Errorf("failed %s should carry the denial marker: %q", name, line)
		}
	}
}
