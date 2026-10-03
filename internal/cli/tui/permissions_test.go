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

// TestReviewNoteFilesAboveItsCard verifies the codex order: the card is
// announced while the call streams, and the note (published later by the gate)
// is inserted directly above that card instead of landing below it.
func TestReviewNoteFilesAboveItsCard(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(60, 20)
	tr.addToolCard(&toolCard{id: "call_9", name: "bash", input: map[string]any{"command": "git commit -m x"}, state: cardSuccess})
	tr.addReviewNote(judge.Note{
		Tool: "bash", ToolCallID: "call_9", Kind: judge.NoteApproved,
		Risk: "medium", Authorization: "high", Rationale: "常规提交", Lang: "zh",
	})
	text := strings.Join(tr.contentLines(), "\n")
	noteAt := strings.Index(text, "自动审批通过")
	cardAt := strings.Index(text, "commit")
	if noteAt < 0 || cardAt < 0 {
		t.Fatalf("missing note or card:\n%s", text)
	}
	if noteAt > cardAt {
		t.Errorf("note must render above its card (note@%d card@%d)\n%s", noteAt, cardAt, text)
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
