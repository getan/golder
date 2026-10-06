package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

// TestPermissionsPickerOpens verifies bare /permissions opens the four-mode
// picker (codex-style presets), that the picker stays English even in a
// Chinese conversation, and that confirming a row switches the live state.
func TestPermissionsPickerOpens(t *testing.T) {
	s := newRemoteTestSession(t)
	m := NewModel(Options{})
	m.session = s
	m.slash = s.slash
	s.agentCtx.Messages = append(s.agentCtx.Messages, userText("帮我看看权限"))

	got, cmd := m.runSlash("/permissions")
	if cmd != nil {
		t.Fatal("opening the picker should not start a run")
	}
	gm := got.(Model)
	if !gm.menu.picking() || gm.menu.pickKind != "permissions" {
		t.Fatalf("picker not open: picking=%v kind=%q", gm.menu.picking(), gm.menu.pickKind)
	}
	if len(gm.menu.pick) != 4 {
		t.Fatalf("picker rows = %d, want 4", len(gm.menu.pick))
	}
	if gm.menu.pick[0].Value != "read-only" || gm.menu.pick[3].Value != "full-access" {
		t.Errorf("picker values = %+v", gm.menu.pick)
	}
	if gm.menu.pick[0].Title != "Read Only" || gm.menu.pick[2].Title != "Auto (LLM review)" {
		t.Errorf("picker titles must stay English: %+v", gm.menu.pick)
	}
	if !strings.Contains(gm.menu.pick[0].Detail, "blocked") {
		t.Errorf("picker details must stay English: %+v", gm.menu.pick)
	}
	if !strings.Contains(gm.transcriptText(), "Select a permission mode") {
		t.Errorf("picker prompt must stay English:\n%s", gm.transcriptText())
	}

	// Confirming a row runs /permissions <value> against the session state.
	gm.menu.selected = 0
	got, _ = gm.submitSlashSelected()
	_ = got
	if mode := s.perms.Mode(); mode != permissions.ReadOnly {
		t.Fatalf("mode after pick = %v, want read-only", mode)
	}
}

// TestPermissionsSlashSwitches verifies the argument form and the localized
// output.
func TestPermissionsSlashSwitches(t *testing.T) {
	s := newRemoteTestSession(t)
	m := NewModel(Options{})
	m.session = s
	m.slash = s.slash

	// No conversation yet → English output.
	got, _ := m.runSlash("/permissions full-access")
	gm := got.(Model)
	if mode := s.perms.Mode(); mode != permissions.FullAccess {
		t.Fatalf("mode = %v, want full-access", mode)
	}
	if !strings.Contains(gm.transcriptText(), "permissions switched to") {
		t.Errorf("transcript missing switch message:\n%s", gm.transcriptText())
	}

	// A Chinese prompt flips the output language.
	s.agentCtx.Messages = append(s.agentCtx.Messages, userText("帮我切一下权限"))
	got, _ = gm.runSlash("/permissions auto")
	gm = got.(Model)
	if !strings.Contains(gm.transcriptText(), "权限模式已切换为") {
		t.Errorf("transcript missing Chinese switch message:\n%s", gm.transcriptText())
	}
}

// TestJudgeNoteMsgRenders verifies a gate note lands in the transcript with
// its rationale.
func TestJudgeNoteMsgRenders(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	note := judge.Note{
		Tool:          "bash",
		ToolCallID:    "call_1",
		Kind:          judge.NoteApproved,
		Risk:          "medium",
		Authorization: "high",
		Rationale:     "提交本地 commit，完全可回滚。",
		Lang:          "zh",
	}
	next, _ := m.Update(judgeNoteMsg{note: note})
	view := next.View().Content
	if !strings.Contains(view, "自动审批通过") || !strings.Contains(view, "完全可回滚") {
		t.Errorf("view missing review note:\n%s", view)
	}
}

// TestReviewNoteSitsBelowCommandAboveOutput verifies the gate verdict reads as
// the command's outcome: the note renders directly below the command it judged
// and above the tool's output, instead of landing above the card or below a
// long body. That is the ordering the user asked for ("note 必须紧随命令后").
func TestReviewNoteSitsBelowCommandAboveOutput(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addToolCard(&toolCard{
		id: "call_9", name: "bash", input: map[string]any{"command": "git commit -m x"},
		state: cardSuccess, response: []respNode{{text: "output line"}},
	})
	tr.addReviewNote(judge.Note{
		Tool: "bash", ToolCallID: "call_9", Kind: judge.NoteApproved,
		Risk: "medium", Authorization: "high", Rationale: "常规提交", Lang: "zh",
	})
	text := strings.Join(tr.contentLines(), "\n")
	cmdAt := strings.Index(text, "commit")
	noteAt := strings.Index(text, "自动审批通过")
	outAt := strings.Index(text, "output line")
	if cmdAt < 0 || noteAt < 0 || outAt < 0 {
		t.Fatalf("missing command, note or output:\n%s", text)
	}
	if !(cmdAt < noteAt && noteAt < outAt) {
		t.Errorf("note must sit between the command and its output (cmd@%d note@%d out@%d)\n%s", cmdAt, noteAt, outAt, text)
	}
}

// TestReviewNoteStaysBelowCommandWhenOutputArrives covers the live timing: the
// note lands while the call is still running (no output yet), and the output
// that arrives later must render below the note rather than pushing it away
// from the command.
func TestReviewNoteStaysBelowCommandWhenOutputArrives(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	card := &toolCard{id: "call_9", name: "bash", input: map[string]any{"command": "git commit -m x"}, state: cardRunning}
	tr.addToolCard(card)
	tr.addReviewNote(judge.Note{
		Tool: "bash", ToolCallID: "call_9", Kind: judge.NoteApproved,
		Risk: "medium", Authorization: "high", Rationale: "常规提交", Lang: "zh",
	})
	card.complete(true, "first output line\nsecond output line", nil)
	tr.reflow()
	text := strings.Join(tr.contentLines(), "\n")
	cmdAt := strings.Index(text, "commit")
	noteAt := strings.Index(text, "自动审批通过")
	outAt := strings.Index(text, "first output line")
	if cmdAt < 0 || noteAt < 0 || outAt < 0 {
		t.Fatalf("missing command, note or output:\n%s", text)
	}
	if !(cmdAt < noteAt && noteAt < outAt) {
		t.Errorf("later output must render below the note (cmd@%d note@%d out@%d)\n%s", cmdAt, noteAt, outAt, text)
	}
}

// TestReviewNoteUnknownCardAppends keeps the fallback honest: a note whose
// tool card is not in the transcript (a task child's gate decision) still
// renders, as a standalone block at the end.
func TestReviewNoteUnknownCardAppends(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addToolCard(&toolCard{id: "call_other", name: "bash", input: map[string]any{"command": "echo hi"}, state: cardSuccess})
	tr.addReviewNote(judge.Note{
		Tool: "bash", ToolCallID: "call_child", Kind: judge.NoteDenied,
		Risk: "high", Authorization: "low", Rationale: "子任务被拒", Lang: "zh",
	})
	text := strings.Join(tr.contentLines(), "\n")
	if !strings.Contains(text, "自动审批拒绝") || !strings.Contains(text, "子任务被拒") {
		t.Errorf("standalone note missing:\n%s", text)
	}
}

// userText builds a plain user message for language detection in tests.
func userText(s string) agentcore.Message {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(s)}}
}

// transcriptText joins the transcript's rendered content lines for assertions.
func (m Model) transcriptText() string {
	return strings.Join(m.transcript.contentLines(), "\n")
}
