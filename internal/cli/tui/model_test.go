package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/session"
)

// TestModelQuitKeys verifies Ctrl+D quits idle immediately while Ctrl+C
// needs two presses within the arm window (a single idle press only arms).
func TestModelQuitKeys(t *testing.T) {
	m := NewModel(Options{})
	got, cmd := m.Update(keyPress("ctrl+d"))
	if cmd == nil {
		t.Fatal("ctrl+d: expected a quit command, got nil")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Errorf("ctrl+d: cmd produced %T, want tea.QuitMsg", msg)
	}
	if !got.(Model).quitting {
		t.Error("ctrl+d: model should be marked quitting")
	}

	m = NewModel(Options{})
	got, cmd = m.Update(keyPress("ctrl+c"))
	if cmd != nil {
		t.Errorf("first ctrl+c: expected nil cmd (arm only), got %v", cmd())
	}
	armed := got.(Model)
	if armed.quitting {
		t.Error("first ctrl+c: must not quit")
	}
	if armed.quitArmedAt.IsZero() {
		t.Error("first ctrl+c: should arm the quit")
	}
	_, cmd = armed.Update(keyPress("ctrl+c"))
	if cmd == nil {
		t.Fatal("second ctrl+c: expected a quit command, got nil")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Errorf("second ctrl+c: cmd produced %T, want tea.QuitMsg", msg)
	}
}

// TestModelCtrlCArmExpires verifies an expired arm does not quit: the next
// press re-arms instead.
func TestModelCtrlCArmExpires(t *testing.T) {
	m := NewModel(Options{})
	m.quitArmedAt = time.Now().Add(-time.Hour)
	got, cmd := m.Update(keyPress("ctrl+c"))
	if cmd != nil {
		t.Errorf("expired arm + ctrl+c: expected nil cmd, got %v", cmd())
	}
	if got.(Model).quitting {
		t.Error("expired arm + ctrl+c: must not quit")
	}
	if got.(Model).quitArmedAt.IsZero() {
		t.Error("expired arm + ctrl+c: should re-arm")
	}
}

// TestModelCtrlCClearsDraft verifies the shell-like first stage: Ctrl+C with a
// non-empty composer discards the draft (and its paste/image placeholder
// bodies) without arming a quit or producing a command, and only the later
// presses follow the existing two-stage interrupt/quit path.
func TestModelCtrlCClearsDraft(t *testing.T) {
	var mm tea.Model = apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 10})
	for _, r := range "draft" {
		mm, _ = mm.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	drafted := mm.(Model)
	drafted.pastes = map[int]string{1: "pasted body"}
	drafted.images = map[int]string{1: "/tmp/pasted.png"}

	got, cmd := drafted.Update(keyPress("ctrl+c"))
	cleared := got.(Model)
	if v := cleared.input.Value(); v != "" {
		t.Fatalf("ctrl+c with a draft should clear the composer, got %q", v)
	}
	if cmd != nil {
		t.Errorf("clearing press should not produce a command, got %v", cmd())
	}
	if cleared.quitting {
		t.Error("clearing press must not quit")
	}
	if !cleared.quitArmedAt.IsZero() {
		t.Error("clearing press must not arm the quit")
	}
	if len(cleared.pastes) != 0 || len(cleared.images) != 0 {
		t.Errorf("clearing press should drop placeholder bodies, pastes=%v images=%v", cleared.pastes, cleared.images)
	}

	// Post-clear presses run the normal idle path: first arms, second quits.
	got, cmd = cleared.Update(keyPress("ctrl+c"))
	armed := got.(Model)
	if cmd != nil {
		t.Errorf("post-clear ctrl+c: expected nil cmd (arm only), got %v", cmd())
	}
	if armed.quitArmedAt.IsZero() {
		t.Fatal("post-clear ctrl+c should arm the quit")
	}
	_, cmd = armed.Update(keyPress("ctrl+c"))
	if cmd == nil {
		t.Fatal("second post-clear ctrl+c: expected a quit command, got nil")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Errorf("second post-clear ctrl+c: cmd produced %T, want tea.QuitMsg", msg)
	}
}

// TestModelCtrlCWhileRunningInterruptsWithDraft pins the precedence: an
// in-flight run owns Ctrl+C, so a stray buffer (typing is gated while running;
// this is only a safety net) never absorbs the interrupt.
func TestModelCtrlCWhileRunningInterruptsWithDraft(t *testing.T) {
	m := NewModel(Options{})
	interrupted := false
	m.interruptFn = func() { interrupted = true }
	m.running = true
	m.input.SetValue("stray")
	got, _ := m.Update(keyPress("ctrl+c"))
	if !interrupted {
		t.Fatal("ctrl+c while running must interrupt even with a non-empty composer")
	}
	if got.(Model).input.Value() != "stray" {
		t.Error("interrupt must not clear the composer")
	}
}

// TestModelViewShell verifies the empty shell renders on the alt-screen and,
// once a size is known, occupies the full terminal height (empty transcript rows
// + status bar + input line), with the real status bar (#386) painting its
// fields.
func TestModelViewShell(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	view := next.View()
	if !view.AltScreen {
		t.Error("View should request the alt-screen")
	}
	if got := strings.Count(view.Content, "\n"); got != 9 {
		t.Errorf("newline count = %d, want 9 (10 rows)", got)
	}
	if !strings.Contains(view.Content, "test-model") {
		t.Errorf("status bar model field missing from view: %q", view.Content)
	}
}

// TestModelNewlineKeys verifies that Shift+Enter inserts a line break at the
// cursor and preserves the already-typed text, rather than submitting. Plain
// Enter still submits, so it does not leave a newline in the buffer. Shift+Enter
// is the primary newline key (reported distinctly by terminals speaking the
// Kitty disambiguate protocol, which Bubble Tea enables by default); Ctrl+J and
// Alt+Enter are fallbacks for terminals that collapse Shift+Enter to a bare CR.
func TestModelNewlineKeys(t *testing.T) {
	var mm tea.Model = NewModel(Options{})
	mm, _ = mm.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	for _, r := range "abc" {
		mm, _ = mm.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	mm, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	for _, r := range "def" {
		mm, _ = mm.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := mm.(Model).input.Value(); got != "abc\ndef" {
		t.Errorf("input = %q, want %q", got, "abc\ndef")
	}
}

// TestModelSelectionCopy drives a mouse selection over a transcript line and
// asserts Ctrl+C copies the selected text (over OSC52) and clears the selection.
func TestModelSelectionCopy(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m.transcript.addUser("hello world")

	// Locate the rendered screen cell where the text begins so the test does not
	// hard-code the transcript's bottom-stick row.
	content, _ := m.renderContent()
	rows := strings.Split(content, "\n")
	y, x := -1, -1
	for i, r := range rows {
		plain := stripANSI(r)
		if idx := strings.Index(plain, "hello world"); idx >= 0 {
			y = i
			x = ui.Width(plain[:idx])
			break
		}
	}
	if y < 0 {
		t.Fatal("rendered screen did not contain the transcript text")
	}

	// Select exactly "hello world" (11 display cells) on that row.
	m.sel = selection{active: true, anchor: point{x, y}, cursor: point{x + 11, y}}
	next, cmd := m.Update(keyPress("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c with a selection should emit a clipboard command")
	}
	if got := fmt.Sprintf("%s", cmd()); got != "hello world" {
		t.Errorf("copied %q, want %q", got, "hello world")
	}
	if !next.(Model).sel.empty() {
		t.Error("selection should be cleared after Ctrl+C copies it")
	}
}

// TestModelCtrlCFallsBackToQuit verifies Ctrl+C with no selection keeps its
// interrupt/quit role (idle → quit).
func TestModelCtrlCFallsBackToQuit(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	next, cmd := m.Update(keyPress("ctrl+c"))
	if cmd != nil {
		t.Fatal("first ctrl+c without a selection should only arm, not quit")
	}
	next, cmd = next.Update(keyPress("ctrl+c"))
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatal("second ctrl+c without a selection should quit when idle")
	}
	if !next.(Model).quitting {
		t.Error("model should be marked quitting")
	}
}

// TestModelImagePasteInsertsPlaceholder verifies a clipboard image (already saved
// to a temp file) is stashed and shown in the composer as a compact "[Image #N]"
// placeholder, and that expandImages swaps it for an "@image:<path>" reference at
// submit so BuildUserContent attaches it as multimodal content.
func TestModelImagePasteInsertsPlaceholder(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	next, _ := m.Update(clipboardImageMsg{path: "/tmp/golder-clip-1.png", ok: true})
	m = next.(Model)

	if got, want := m.input.Value(), "[Image #1]"; got != want {
		t.Errorf("composer showed %q, want placeholder %q", got, want)
	}
	if got := m.expandImages(m.input.Value()); got != "@image:/tmp/golder-clip-1.png" {
		t.Errorf("expandImages = %q, want the @image reference", got)
	}
}

// TestModelImagePasteFallsBackToText verifies an empty clipboard image reply
// (ok=false) falls back to an OSC52 text read rather than inserting anything.
func TestModelImagePasteFallsBackToText(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	next, cmd := m.Update(clipboardImageMsg{ok: false})
	if cmd == nil {
		t.Fatal("no image on the clipboard should fall back to a text read command")
	}
	if got := next.(Model).input.Value(); got != "" {
		t.Errorf("composer should stay empty on fallback, got %q", got)
	}
}

// TestModelExpandImagesUnknownID verifies an unknown image id is left untouched.
func TestModelExpandImagesUnknownID(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, clipboardImageMsg{path: "/tmp/a.png", ok: true})

	got := m.expandImages("see [Image #1] and [Image #7]")
	want := "see @image:/tmp/a.png and [Image #7]"
	if got != want {
		t.Errorf("expandImages = %q, want %q", got, want)
	}
}

// keyPress builds a KeyPressMsg matching String()==s for the simple keys used
// in these tests (ctrl+<letter>).
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+y":
		return tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
	case "super+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper}
	case "super+v":
		return tea.KeyPressMsg{Code: 'v', Mod: tea.ModSuper}
	default:
		return tea.KeyPressMsg{}
	}
}

// TestModelPasteSingleLineInsertsVerbatim verifies a single-line bracketed paste
// is inserted into the editor as-is (no placeholder collapsing).
func TestModelPasteSingleLineInsertsVerbatim(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, tea.PasteMsg{Content: "hello world"})
	if got := m.input.Value(); got != "hello world" {
		t.Errorf("input after paste = %q, want %q", got, "hello world")
	}
}

// TestModelPasteMultilineCollapses verifies a multi-line paste is collapsed to a
// compact "[Pasted text #N +M lines]" placeholder in the composer (Claude Code
// style) while the full body is stashed and expanded back at submit.
func TestModelPasteMultilineCollapses(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, tea.PasteMsg{Content: "line1\nline2\nline3"})

	if got, want := m.input.Value(), "[Pasted text #1 +3 lines]"; got != want {
		t.Errorf("composer showed %q, want placeholder %q", got, want)
	}
	if got := m.expandPastes(m.input.Value()); got != "line1\nline2\nline3" {
		t.Errorf("expandPastes = %q, want the original body", got)
	}
}

// TestModelExpandPastesMultiple verifies several collapsed pastes each expand
// back to their own body, and an unknown id is left untouched.
func TestModelExpandPastesMultiple(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, tea.PasteMsg{Content: "aaa\nbbb"})
	m = apply(t, m, tea.PasteMsg{Content: "ccc\nddd"})

	got := m.expandPastes("x [Pasted text #1 +2 lines] y [Pasted text #2 +2 lines] [Pasted text #9 +9 lines]")
	want := "x aaa\nbbb y ccc\nddd [Pasted text #9 +9 lines]"
	if got != want {
		t.Errorf("expandPastes = %q, want %q", got, want)
	}
}

// TestModelClipboardReadInsertsIntoInput verifies an OSC52 clipboard read reply
// (tea.ClipboardMsg, the response to Ctrl+V) is inserted into the editor.
func TestModelClipboardReadInsertsIntoInput(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	m = apply(t, m, tea.ClipboardMsg{Content: "pasted"})
	if got := m.input.Value(); got != "pasted" {
		t.Errorf("input after clipboard read = %q, want %q", got, "pasted")
	}
}

// TestModelCopyToClipboard verifies Ctrl+Y emits an OSC52 SetClipboard command
// carrying the current buffer, and is a no-op on an empty buffer.
func TestModelCopyToClipboard(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	// Empty buffer: no command.
	if _, cmd := m.Update(keyPress("ctrl+y")); cmd != nil {
		t.Errorf("ctrl+y on empty buffer should be a no-op, got a command")
	}

	m = apply(t, m, tea.PasteMsg{Content: "copy me"})
	_, cmd := m.Update(keyPress("ctrl+y"))
	if cmd == nil {
		t.Fatal("ctrl+y with content should emit a clipboard command")
	}
	// SetClipboard yields an unexported string-underlying message; format it to
	// read its payload without depending on the tea-internal type.
	if got := fmt.Sprintf("%s", cmd()); got != "copy me" {
		t.Errorf("clipboard command carried %q, want %q", got, "copy me")
	}
}

// TestModelSuperCCopiesSelection verifies Cmd+C (super+c) copies the mouse
// selection just like Ctrl+C, but with an empty buffer and no selection it is a
// no-op rather than quitting — Cmd+C is "copy" on macOS, never interrupt/quit.
func TestModelSuperCCopiesSelection(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})

	// No selection, empty buffer: no-op, and never a quit command.
	if next, cmd := m.Update(keyPress("super+c")); cmd != nil {
		t.Errorf("super+c with nothing to copy should be a no-op, got a command")
	} else if next.(Model).quitting {
		t.Error("super+c must never quit")
	}

	// No selection, non-empty buffer: copies the whole buffer.
	m = apply(t, m, tea.PasteMsg{Content: "buffer text"})
	if _, cmd := m.Update(keyPress("super+c")); cmd == nil {
		t.Fatal("super+c with buffer content should emit a clipboard command")
	} else if got := fmt.Sprintf("%s", cmd()); got != "buffer text" {
		t.Errorf("super+c copied %q, want %q", got, "buffer text")
	}
}

// TestModelSuperVPastes verifies Cmd+V (super+v) requests the clipboard over
// OSC52 when idle, like Ctrl+V.
func TestModelSuperVPastes(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 40, Height: 12})
	if _, cmd := m.Update(keyPress("super+v")); cmd == nil {
		t.Fatal("super+v should emit a clipboard read command when idle")
	}
}

// TestModelSubagentPanelLifecycle drives the sub-agent status panel through a
// task tool's lifecycle on the running model: a toolStartMsg(name=="task") opens
// a row, subagentProgressMsg refreshes it and it appears in the rendered View
// above the input, and the task's toolEndMsg retires it (empty panel → no rows).
func TestModelSubagentPanelLifecycle(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true // the panel only renders while a run is in flight
	m.spinner.begin(time.Now(), "")

	m = apply(t, m, toolStartMsg{id: "task-1", name: "task", input: map[string]any{"description": "build parser"}})
	if got := m.subagents.active(); got != 1 {
		t.Fatalf("active after task start = %d, want 1", got)
	}

	m = apply(t, m, subagentProgressMsg{id: "task-1", desc: "build parser", activity: "Editing", tokens: 64})
	if row := m.subagents.byID["task-1"]; row == nil || row.activity != "Editing" {
		t.Fatalf("row after progress = %+v, want activity=Editing", row)
	}
	// The panel line is identified by its ⏺ glyph (distinct from the tool card,
	// which also mentions the description) plus the live activity.
	if view := m.View().Content; !strings.Contains(view, "⏺") || !strings.Contains(view, "Editing") {
		t.Errorf("view missing panel line: %q", view)
	}

	// A non-task tool must not open a panel row.
	m = apply(t, m, toolStartMsg{id: "read-1", name: "read_file", input: map[string]any{"path": "/x"}})
	if got := m.subagents.active(); got != 1 {
		t.Errorf("active after non-task start = %d, want 1", got)
	}

	m = apply(t, m, toolEndMsg{id: "task-1", ok: true, result: "done"})
	if got := m.subagents.active(); got != 0 {
		t.Errorf("active after task end = %d, want 0", got)
	}
	if view := m.View().Content; strings.Contains(view, "⏺") {
		t.Errorf("view still shows retired panel line: %q", view)
	}
}

// TestModelCompactionIndicator verifies compactionStartMsg pins the spinner to
// "Compacting conversation…" while summarization runs, and compactionMsg clears
// the label and records the "(context compacted)" system note.
func TestModelCompactionIndicator(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	m.spinner.begin(time.Now(), "")

	m = apply(t, m, compactionStartMsg{})
	if view := stripANSI(m.spinner.view(120)); !strings.Contains(view, "Compacting conversation…") {
		t.Errorf("spinner view %q should show the compaction label", view)
	}

	m = apply(t, m, compactionMsg{})
	if m.spinner.pinned != "" {
		t.Errorf("compactionMsg should unpin the spinner, got %q", m.spinner.pinned)
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, "(context compacted)") {
		t.Errorf("transcript should note the compaction, got:\n%s", joined)
	}
}

// TestModelSubagentPanelHeightReservation verifies the panel's rows are reserved
// out of the transcript height so the total shell height is unchanged whether or
// not sub-agents are active.
func TestModelSubagentPanelHeightReservation(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	m.spinner.begin(time.Now(), "")
	m.relayout()
	base := m.transcript.viewportHeight()

	m = apply(t, m, toolStartMsg{id: "t1", name: "task", input: map[string]any{"description": "a"}})
	m = apply(t, m, toolStartMsg{id: "t2", name: "task", input: map[string]any{"description": "b"}})
	if got := m.transcript.viewportHeight(); got != base-2 {
		t.Errorf("transcript height with 2 panel rows = %d, want %d (base %d - 2)", got, base-2, base)
	}
	// Every rendered frame stays exactly Height rows tall regardless of the panel.
	if got := strings.Count(m.View().Content, "\n"); got != 19 {
		t.Errorf("newline count = %d, want 19 (20 rows)", got)
	}
}

// TestModelSubagentPanelNavigation verifies that while a run streams and the
// composer is empty, ↓/↑ move the panel cursor, Enter expands the selected row's
// accumulated output inline (fed by tool-update deltas), and Esc collapses/clears
// the selection rather than interrupting the run.
func TestModelSubagentPanelNavigation(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.running = true
	m.spinner.begin(time.Now(), "")
	m = apply(t, m, toolStartMsg{id: "a", name: "task", input: map[string]any{"description": "task A"}})
	m = apply(t, m, toolStartMsg{id: "b", name: "task", input: map[string]any{"description": "task B"}})
	// A sub-agent's forwarded text arrives as an incremental tool-update delta.
	m = apply(t, m, toolUpdateMsg{id: "b", partial: "output of B"})

	// ↓ selects the top row, a second ↓ moves to row b.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.subagents.hasSelection() || m.subagents.selected != 0 {
		t.Fatalf("after down: selected=%d hasSel=%v, want 0/true", m.subagents.selected, m.subagents.hasSelection())
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})

	// Enter expands the selected row; its accumulated output shows in the render.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.subagents.expandedID(); got != "b" {
		t.Fatalf("expandedID after enter = %q, want b", got)
	}
	if !strings.Contains(renderContentStr(m), "output of B") {
		t.Errorf("expanded render missing sub-agent output:\n%s", renderContentStr(m))
	}

	// Esc collapses/clears the selection and does NOT quit (no quit command).
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.subagents.hasSelection() {
		t.Error("esc should clear the panel selection")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("esc with an active selection should not quit the program")
		}
	}
}

// TestModelSubagentEscReturnsToInput verifies the one-key escape ("escape hatch: one key back to the input box"):
// while a sub-agent runs the composer is blurred (no typing), and after arrowing
// into the panel a single Esc both clears the selection and re-focuses the input
// box — so returning to the composer never requires more than one press and never
// interrupts the run.
func TestModelSubagentEscReturnsToInput(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.running = true
	m.spinner.begin(time.Now(), "")
	m.input.Blur() // the composer is blurred for the duration of a run (startPrompt)
	m = apply(t, m, toolStartMsg{id: "a", name: "task", input: map[string]any{"description": "task A"}})

	// Arrow into the panel: a selection is now active while the input stays blurred.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.subagents.hasSelection() {
		t.Fatal("down should select a sub-agent row")
	}
	if m.input.Focused() {
		t.Fatal("the composer should be blurred while a sub-agent run streams")
	}

	// One Esc escapes: selection cleared AND the input box re-focused, in a single
	// press, without interrupting the run.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.subagents.hasSelection() {
		t.Error("one Esc should clear the panel selection")
	}
	if !m.input.Focused() {
		t.Error("one Esc should re-focus the input box (return to the composer)")
	}
	if !m.running {
		t.Error("escaping the panel selection must not interrupt the run")
	}
}

// TestModelPromptHistoryNavigation verifies shell-like prompt history: after
// submitting two prompts, ↑ from an empty composer recalls the most recent, a
// second ↑ walks further back, ↓ walks forward again, and a final ↓ restores the
// (empty) live draft. With no run starter wired, submit records history and
// leaves the composer idle/focused, so the arrow keys route to historyPrev /
// historyNext.
func TestModelPromptHistoryNavigation(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 12})

	submit := func(s string) {
		for _, r := range s {
			m = apply(t, m, runeKey(r))
		}
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	submit("first prompt")
	submit("second prompt")

	if m.input.Value() != "" {
		t.Fatalf("composer should be empty after submit, got %q", m.input.Value())
	}

	// ↑ recalls the newest entry, a second ↑ the older one.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "second prompt" {
		t.Errorf("first ↑ recalled %q, want %q", got, "second prompt")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "first prompt" {
		t.Errorf("second ↑ recalled %q, want %q", got, "first prompt")
	}

	// ↓ walks forward to the newer entry, then past it to restore the live draft.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.input.Value(); got != "second prompt" {
		t.Errorf("↓ walked to %q, want %q", got, "second prompt")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Errorf("↓ past newest should restore the empty draft, got %q", got)
	}
}

// TestModelPromptHistoryDedupsAndStashesDraft verifies two shell-like behaviors:
// a consecutive-duplicate submit is not stored twice, and an in-progress draft is
// stashed when browsing begins so ↓ past the newest entry brings it back.
func TestModelPromptHistoryDedupsAndStashesDraft(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 12})

	submit := func(s string) {
		for _, r := range s {
			m = apply(t, m, runeKey(r))
		}
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	submit("same")
	submit("same") // consecutive duplicate — must not be stored twice
	if len(m.history) != 1 {
		t.Fatalf("history = %v, want a single deduped entry", m.history)
	}

	// Type a fresh draft, then browse: ↑ stashes the draft and recalls history,
	// ↓ past the newest entry restores the stashed draft verbatim.
	for _, r := range "draft" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "same" {
		t.Errorf("↑ recalled %q, want %q", got, "same")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.input.Value(); got != "draft" {
		t.Errorf("↓ should restore the stashed draft %q, got %q", "draft", got)
	}
}

// TestResumeSessionGuards verifies /resume refuses while a run is in flight
// and reports cleanly with no active session.
func TestResumeSessionGuards(t *testing.T) {
	running := NewModel(Options{})
	running.running = true
	got, _ := running.runSlash("/resume abc")
	joined := strings.Join(blockTexts(got.(Model).transcript), "\n")
	if !strings.Contains(joined, "Interrupt the current run") {
		t.Errorf("running /resume should refuse, got %q", joined)
	}

	m := NewModel(Options{})
	got, _ = m.runSlash("/resume abc")
	joined = strings.Join(blockTexts(got.(Model).transcript), "\n")
	if !strings.Contains(joined, "No active session") {
		t.Errorf("session-less /resume should report, got %q", joined)
	}
}

// TestModelPickerSelectSwitches drives the bare-/model picker end to end:
// cached catalog opens the picker synchronously, arrows move, and Enter moves
// to the reasoning-level stage (the codex-style second step) instead of
// switching immediately.
func TestModelPickerSelectSwitches(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	m := NewModel(Options{})
	m.session = &runSession{}
	m.live.FetchedModels = []string{"m-a", "m-b"}
	m.live.Model = "m-a"
	m.live.ProviderName = "openai"

	got, cmd := m.runSlash("/model")
	if cmd != nil {
		t.Fatalf("cached picker: expected nil cmd, got %T", cmd)
	}
	gm := got.(Model)
	if !gm.menu.picking() {
		t.Fatal("bare /model should open the picker")
	}
	gm.menu.moveDown()
	got2, _ := gm.submitSlashSelected()
	gm2 := got2.(Model)
	if !gm2.menu.picking() || gm2.menu.pickKind != "model-level" {
		t.Fatalf("stage 1 confirm should open the level picker, picking=%v kind=%q", gm2.menu.picking(), gm2.menu.pickKind)
	}
	if gm2.menu.pickModel != "m-b" {
		t.Errorf("pending model = %q, want m-b", gm2.menu.pickModel)
	}
	if gm2.live.Model != "m-a" {
		t.Errorf("stage 1 must not switch before the level confirm, live.Model = %q", gm2.live.Model)
	}
}

// TestModelPickerTwoStageConfirm drives both stages: choosing a model opens
// the levels (with the current one marked), confirming a level switches the
// model and applies the level in one step, and Esc on stage 2 cancels the
// whole flow.
func TestModelPickerTwoStageConfirm(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	m := NewModel(Options{})
	m.session = &runSession{}
	m.live.FetchedModels = []string{"m-a", "m-b"}
	m.live.Model = "m-a"
	m.live.ProviderName = "openai"
	m.live.ThinkingLevel = agentcore.ThinkingHigh

	got, _ := m.runSlash("/model")
	gm := got.(Model)
	gm.menu.moveDown() // m-b
	got2, _ := gm.submitSlashSelected()
	gm2 := got2.(Model)
	if gm2.menu.pickMark != "high" {
		t.Errorf("level picker mark = %q, want the current level high", gm2.menu.pickMark)
	}

	// Esc cancels stage 2 and leaves the model untouched.
	canceled := gm2
	canceled.menu.close()
	if canceled.menu.picking() || canceled.live.Model != "m-a" || canceled.live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("Esc cancel changed state: picking=%v model=%q level=%q",
			canceled.menu.picking(), canceled.live.Model, canceled.live.ThinkingLevel)
	}

	// Confirm the highlighted level (the fallback ladder starts at low ...).
	gm2.menu.moveDown()
	gm2.menu.moveDown() // high
	got3, cmd := gm2.submitSlashSelected()
	gm3 := got3.(Model)
	if cmd != nil {
		t.Fatalf("stage 2 confirm returned cmd %T, want nil", cmd)
	}
	if gm3.live.Model != "m-b" {
		t.Errorf("live.Model = %q, want m-b", gm3.live.Model)
	}
	if gm3.live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("live.ThinkingLevel = %q, want high", gm3.live.ThinkingLevel)
	}
	if gm3.menu.picking() {
		t.Error("picker should close after the level confirm")
	}
}

// TestModelPickerAsyncFetch verifies the uncached path returns a fetch Cmd
// (no blocking) and surfaces fetch errors without opening the picker.
func TestModelPickerAsyncFetch(t *testing.T) {
	m := NewModel(Options{})
	m.session = &runSession{}
	m.live.ProviderName = "" // unknown provider: fetch fails without network

	got, cmd := m.runSlash("/model")
	if cmd == nil {
		t.Fatal("uncached picker: expected a fetch cmd, got nil")
	}
	gm := got.(Model)
	if gm.menu.picking() {
		t.Fatal("picker must not open before the fetch lands")
	}
	msg := cmd()
	fm, ok := msg.(modelsFetchedMsg)
	if !ok {
		t.Fatalf("fetch cmd produced %T, want modelsFetchedMsg", msg)
	}
	if fm.err == nil {
		t.Fatal("fetch against unknown provider should error")
	}
	got2, _ := gm.Update(fm)
	if got2.(Model).menu.picking() {
		t.Error("picker must not open on fetch error")
	}
	joined := strings.Join(blockTexts(got2.(Model).transcript), "\n")
	if !strings.Contains(joined, "/model <id>") {
		t.Errorf("error should hint direct switch, got %q", joined)
	}
}

// TestProviderPickerSelectSwitches drives the bare-/provider picker end to
// end: it opens synchronously from the registry (no network), rows carry the
// env var names and a credential marker, arrows move, and Enter switches the
// live provider through the shared /provider action (default model applied).
func TestProviderPickerSelectSwitches(t *testing.T) {
	m := NewModel(Options{})
	m.session = &runSession{}
	m.live.Model = "muse-spark-1.3-contributor"
	m.live.ProviderName = "opencode-go"

	got, cmd := m.runSlash("/provider")
	if cmd != nil {
		t.Fatalf("provider picker: expected nil cmd, got %T", cmd)
	}
	gm := got.(Model)
	if !gm.menu.picking() || gm.menu.pickKind != "provider" {
		t.Fatalf("picker not open: picking=%v kind=%q", gm.menu.picking(), gm.menu.pickKind)
	}
	if gm.menu.pickMark != "opencode-go" {
		t.Errorf("pickMark = %q, want the current provider", gm.menu.pickMark)
	}
	// Rows expose the env var names (value-free) so the picker is self-explanatory.
	found := ""
	for _, it := range gm.menu.pick {
		if it.Value == "deepseek" {
			found = it.Detail
		}
	}
	if !strings.Contains(found, "DEEPSEEK_API_KEY") {
		t.Errorf("deepseek row detail = %q, want the env var name", found)
	}

	// Move from the first row to deepseek (registry order), then confirm.
	for i := 0; i < len(gm.menu.pick); i++ {
		if gm.menu.pick[gm.menu.selected].Value == "deepseek" {
			break
		}
		gm.menu.moveDown()
	}
	got2, _ := gm.submitSlashSelected()
	gm2 := got2.(Model)
	if gm2.live.ProviderName != "deepseek" {
		t.Errorf("live.ProviderName = %q, want deepseek", gm2.live.ProviderName)
	}
	if gm2.live.Model != "deepseek-v4-flash" {
		t.Errorf("live.Model = %q, want the deepseek default model", gm2.live.Model)
	}
	if gm2.menu.picking() {
		t.Error("picker should close after confirm")
	}
}

// TestResumePickerSelectSwitches drives the bare-/resume picker end to end:
// the recent list opens as a picker, and confirming switches the session.
func TestResumePickerSelectSwitches(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()
	mkHeader := func(id, model string, at time.Time) session.SessionHeader {
		return session.SessionHeader{ID: id, CreatedAt: at, UpdatedAt: at, Model: model, Provider: "prov"}
	}
	mkMsgs := func(text string) agentcore.MessageList {
		if text == "" {
			return nil
		}
		return agentcore.MessageList{agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
		}}
	}
	if err := store.Save(mkHeader("sess-a", "m", now), mkMsgs("")); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := store.Save(mkHeader("sess-b", "m", now.Add(time.Second)), mkMsgs("hello b")); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	m := NewModel(Options{})
	m.live = &cli.LiveConfig{Model: "m", ProviderName: "prov"}
	m.session = &runSession{
		store:    store,
		header:   mkHeader("sess-a", "m", now),
		agentCtx: &agentcore.AgentContext{},
		live:     m.live,
	}

	got, _ := m.runSlash("/resume")
	gm := got.(Model)
	if !gm.menu.picking() {
		t.Fatal("bare /resume should open the picker")
	}
	if gm.menu.pickKind != "resume" {
		t.Errorf("pickKind = %q, want resume", gm.menu.pickKind)
	}
	item, ok := gm.menu.pickCurrent()
	if !ok || item != "sess-b" {
		t.Fatalf("initial pick = (%q, %v), want newest sess-b", item, ok)
	}
	got2, _ := gm.submitSlashSelected()
	gm2 := got2.(Model)
	if gm2.session.header.ID != "sess-b" {
		t.Errorf("header.ID = %q, want sess-b", gm2.session.header.ID)
	}
	if gm2.menu.picking() {
		t.Error("picker should close after confirm")
	}
	if len(gm2.session.agentCtx.Messages) != 1 {
		t.Errorf("messages = %d, want 1 replayed", len(gm2.session.agentCtx.Messages))
	}
}

// TestModelAnnounceInterleavesCard verifies codex ordering: a live-announced
// call opens its card between the text streamed before and after it, the later
// executor start completes setup without a second block, and finalize does not
// duplicate the pre-card text.
func TestModelAnnounceInterleavesCard(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 24})

	m = apply(t, m, textDeltaMsg{delta: "checking "})
	m = apply(t, m, toolAnnounceMsg{id: "ws-1", name: "web_search", input: map[string]any{"query": "muse docs"}})
	m = apply(t, m, textDeltaMsg{delta: "found it"})
	m = apply(t, m, toolStartMsg{id: "ws-1", name: "web_search", input: map[string]any{"query": "muse docs"}})
	m = apply(t, m, toolEndMsg{id: "ws-1", ok: true, result: "hosted: web_search(muse docs)"})
	m = apply(t, m, turnEndMsg{msg: agentcore.AssistantMessage{
		Content: agentcore.ContentList{
			agentcore.NewTextContent("checking found it"),
			agentcore.NewServerToolCallContent("ws-1", "web_search", json.RawMessage(`{"query":"muse docs"}`)),
		},
	}})

	// Exactly one tool block for ws-1 despite announce + start.
	cards := 0
	for _, b := range m.transcript.blocks {
		if b.role == roleTool && b.card != nil && b.card.id == "ws-1" {
			cards++
		}
	}
	if cards != 1 {
		t.Errorf("tool blocks for ws-1 = %d, want 1", cards)
	}
	// Block order: pre-card text, card, post-card text (no duplication).
	var order []string
	for _, b := range m.transcript.blocks {
		switch b.role {
		case roleAssistant:
			order = append(order, "text:"+b.text)
		case roleTool:
			order = append(order, "card")
		}
	}
	want := []string{"text:checking ", "card", "text:found it"}
	if fmt.Sprintf("%v", order) != fmt.Sprintf("%v", want) {
		t.Errorf("block order = %v, want %v", order, want)
	}
	if card := m.toolCards["ws-1"]; card == nil || card.state != cardSuccess {
		t.Errorf("card state = %+v, want success", card)
	}
}

// renderContentStr unwraps renderContent's text for assertions that only need
// the string (the input-row offset is covered by dedicated cursor tests).
func renderContentStr(m Model) string {
	s, _ := m.renderContent()
	return s
}

// TestModelViewCursorAnchorsIME verifies the frame View parks the real cursor
// on the input caret row (the anchor the IME candidate window follows), not at
// the end of the render where the old virtual cursor left it.
func TestModelViewCursorAnchorsIME(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	mm := next.(Model)
	view := mm.View()
	if view.Cursor == nil {
		t.Fatal("View must attach a real cursor while the composer is focused")
	}
	// The caret row is the editor's first text row: find the placeholder line.
	caretRow := -1
	for i, r := range strings.Split(view.Content, "\n") {
		if strings.Contains(stripANSI(r), "Type a message") {
			caretRow = i
			break
		}
	}
	if caretRow < 0 {
		t.Fatalf("placeholder line missing from view:\n%s", view.Content)
	}
	if view.Cursor.Position.Y != caretRow {
		t.Errorf("cursor Y = %d, want caret row %d", view.Cursor.Position.Y, caretRow)
	}
	if view.Cursor.Position.X != 2 {
		t.Errorf("cursor X = %d, want 2 (after prompt)", view.Cursor.Position.X)
	}
}
