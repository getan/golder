package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

// sessionWithTurns builds a session-bound model with two user/assistant turns
// already in the live context, so session-tree and fork commands have history.
func sessionWithTurns(t *testing.T) (Model, *runSession) {
	t.Helper()
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	s.agentCtx.Messages = agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q1")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a1")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q2")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a2")}},
	}
	if err := s.persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	return NewModel(Options{}).withSession(s, nil), s
}

// TestTUIExportWritesSession drives /export <path>: the session file is written
// and the transcript confirms the entry count.
func TestTUIExportWritesSession(t *testing.T) {
	m, s := sessionWithTurns(t)
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	m = typeCommand(t, m, "/export "+path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export file not written: %v", err)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "exported") || !strings.Contains(joined, path) {
		t.Errorf("export confirmation missing; transcript:\n%s", joined)
	}
	if m.session.header.ID != s.header.ID {
		t.Error("export must not switch sessions")
	}
}

// TestTUIImportRoundTrip drives /export then /import: the imported session is a
// fresh session carrying the exported messages.
func TestTUIImportRoundTrip(t *testing.T) {
	m, s := sessionWithTurns(t)
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if _, err := s.store.Export(s.header.ID, path); err != nil {
		t.Fatalf("Export: %v", err)
	}
	oldID := s.header.ID
	m = typeCommand(t, m, "/import "+path)

	if m.session.header.ID == oldID {
		t.Error("/import should switch to a new session")
	}
	if got := len(m.session.agentCtx.Messages); got != 4 {
		t.Errorf("imported message count = %d, want 4", got)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "Imported session") {
		t.Errorf("import confirmation missing; transcript:\n%s", joined)
	}
}

// TestTUICloneSwitchesToNewBranch drives /clone: a new independent session is
// created with the same conversation and becomes active.
func TestTUICloneSwitchesToNewBranch(t *testing.T) {
	m, s := sessionWithTurns(t)
	oldID := s.header.ID
	m = typeCommand(t, m, "/clone")

	if m.session.header.ID == oldID {
		t.Fatal("/clone should switch to the cloned session")
	}
	if got := len(m.session.agentCtx.Messages); got != 4 {
		t.Errorf("cloned message count = %d, want 4", got)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "Cloned session") {
		t.Errorf("clone confirmation missing; transcript:\n%s", joined)
	}
}

// TestTUIForkPickerListsUserMessages drives bare /fork: the picker opens over
// the historical user messages with one row per message.
func TestTUIForkPickerListsUserMessages(t *testing.T) {
	m, _ := sessionWithTurns(t)
	m = typeCommand(t, m, "/fork")

	if m.menu.pickKind != "fork" {
		t.Fatalf("fork picker did not open (kind=%q)", m.menu.pickKind)
	}
	if len(m.menu.pick) != 2 {
		t.Fatalf("fork picker rows = %d, want 2", len(m.menu.pick))
	}
	if !strings.Contains(m.menu.pick[0].Title, "q1") {
		t.Errorf("first fork row = %q, want it to show the first user message", m.menu.pick[0].Title)
	}
}

// TestTUITreePickerListsBranches drives bare /tree: the branch picker opens with
// the current leaf marked.
func TestTUITreePickerListsBranches(t *testing.T) {
	m, _ := sessionWithTurns(t)
	m = typeCommand(t, m, "/tree")

	if m.menu.pickKind != "tree" {
		t.Fatalf("tree picker did not open (kind=%q)", m.menu.pickKind)
	}
	if len(m.menu.pick) == 0 {
		t.Fatal("tree picker should list the session's entries")
	}
	if m.menu.pickMark == "" {
		t.Error("tree picker should mark the active leaf")
	}
}

// TestTUIGoalStateActions verifies the goal state subcommands render in the TUI
// without starting a run: a bare /goal reports no goal, /goal pause reports
// nothing to pause.
func TestTUIGoalStateActions(t *testing.T) {
	m, _ := sessionWithTurns(t)
	m = typeCommand(t, m, "/goal")
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "no goal set") {
		t.Errorf("bare /goal should report no goal; transcript:\n%s", joined)
	}
	if m.running {
		t.Error("bare /goal must not start a run")
	}

	m = typeCommand(t, m, "/goal pause")
	joined = strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "no active goal to pause") {
		t.Errorf("/goal pause should report nothing to pause; transcript:\n%s", joined)
	}
}

// TestTUIBtwBareUsage verifies a bare /btw with no previous side thread prints
// the usage hint instead of starting a run.
func TestTUIBtwBareUsage(t *testing.T) {
	m, _ := sessionWithTurns(t)
	m = typeCommand(t, m, "/btw")
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "usage: /btw") {
		t.Errorf("bare /btw should print usage; transcript:\n%s", joined)
	}
	if m.running {
		t.Error("bare /btw without history must not start a run")
	}
}

// TestTUIRewindEmpty verifies /rewind with no restore points reports so without
// starting a run.
func TestTUIRewindEmpty(t *testing.T) {
	m, _ := sessionWithTurns(t)
	m = typeCommand(t, m, "/rewind")
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "no restore points yet") {
		t.Errorf("/rewind should report no restore points; transcript:\n%s", joined)
	}
	if m.running {
		t.Error("/rewind must not start a run")
	}
}

// TestTUISwitchBranchIndex verifies switching to an earlier tree node rebuilds
// the live context from that node's root→leaf path.
func TestTUISwitchBranchIndex(t *testing.T) {
	store := newTestStore(t)
	root := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q1")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a1")}},
	}
	id := saveSession(t, store, root)
	h, entries, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	// Branch from the root user message with a different second turn.
	if _, err := store.AppendBranch(h, entries[0].ID, agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q2b")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a2b")}},
	}); err != nil {
		t.Fatalf("AppendBranch: %v", err)
	}
	s, _, err := newRunSessionWithStore(store, Options{ResumeID: id})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}

	// Switch to the first tree node (the root user message): the context
	// collapses to that single message.
	msgs, err := s.switchBranchIndex(1)
	if err != nil {
		t.Fatalf("switchBranchIndex: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("switched context has %d messages, want 1", len(msgs))
	}
	if s.curLeaf == "" || s.curLeaf != msgsLeafID(t, store, id, 1) {
		t.Errorf("curLeaf = %q, want the first node id", s.curLeaf)
	}
}

// msgsLeafID returns the id of the n-th entry of the session's stored path.
func msgsLeafID(t *testing.T, store *session.Store, id string, n int) string {
	t.Helper()
	_, entries, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	return entries[n-1].ID
}

// TestTUISlashMenuShowsCategoryHeaders verifies the command popup groups
// candidates under their category labels.
func TestTUISlashMenuShowsCategoryHeaders(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	view := m.menu.view(80)
	// The visible window is capped at maxMenuRows, so only the leading groups
	// fit; the first category block must still be labeled.
	for _, want := range []string{"General", "Session"} {
		if !strings.Contains(view, want) {
			t.Errorf("menu view missing category header %q:\n%s", want, view)
		}
	}
	// Every rendered line is accounted for by rows() so the layout reserves the
	// right height.
	if got := strings.Count(view, "\n") + 1; got != m.menu.rows() {
		t.Errorf("rendered lines = %d, rows() = %d", got, m.menu.rows())
	}
	// A filtered prefix keeps grouping: "m" narrows to Model and Memory.
	m = typeInto(t, NewModel(Options{}), "/m").(Model)
	view = m.menu.view(80)
	for _, want := range []string{"Model", "Memory"} {
		if !strings.Contains(view, want) {
			t.Errorf("filtered menu missing category header %q:\n%s", want, view)
		}
	}
}
