package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/session"
)

func resumeTestModel(t *testing.T) (Model, string, string) {
	t.Helper()
	store := newTestStore(t)
	idA := saveSessionWith(t, store, "model-a", "prov", nil)
	idB := saveSessionWith(t, store, "model-b", "prov", agentcore.MessageList{userMsg("hi b")})
	s := &runSession{
		store:    store,
		header:   session.SessionHeader{ID: idA, Model: "model-a", Provider: "prov"},
		agentCtx: &agentcore.AgentContext{},
		live:     &cli.LiveConfig{Model: "model-a", ProviderName: "prov"},
	}
	m := NewModel(Options{}).withSession(s, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, idA, idB
}

func TestResumeLoadIsAsyncAndCommitsOnResult(t *testing.T) {
	m, _, idB := resumeTestModel(t)
	mm, cmd := m.resumeSession(idB)
	m = mm.(Model)
	if !m.resume.pending {
		t.Fatal("the resume load did not start")
	}
	if !strings.Contains(strings.Join(blockTexts(m.transcript), "\n"), "Resuming session "+idB) {
		t.Fatal("no visible feedback while the session loads")
	}
	if cmd == nil {
		t.Fatal("no background load command")
	}
	msg := cmd()
	if _, ok := msg.(resumeLoadedMsg); !ok {
		t.Fatalf("load returned %T", msg)
	}
	m = apply(t, m, msg)
	if m.session.header.ID != idB {
		t.Fatalf("active session = %q, want %q", m.session.header.ID, idB)
	}
	if m.resume.pending {
		t.Fatal("the completed load left the resume pending")
	}
}

func TestResumeCancelKeepsTheOldSession(t *testing.T) {
	m, idA, idB := resumeTestModel(t)
	mm, cmd := m.resumeSession(idB)
	m = mm.(Model)
	m = m.cancelResume()
	if m.resume.pending {
		t.Fatal("cancel left the resume pending")
	}
	m = apply(t, m, cmd())
	if m.session.header.ID != idA {
		t.Fatalf("a canceled load switched to %q", m.session.header.ID)
	}
	if !strings.Contains(strings.Join(blockTexts(m.transcript), "\n"), "Resume canceled.") {
		t.Fatal("cancel was not announced")
	}
}

func TestResumeEscCancelsTheLoad(t *testing.T) {
	m, _, idB := resumeTestModel(t)
	mm, _ := m.resumeSession(idB)
	m = mm.(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.resume.pending {
		t.Fatal("esc did not cancel the load")
	}
	if !strings.Contains(strings.Join(blockTexts(m.transcript), "\n"), "Resume canceled.") {
		t.Fatal("esc did not announce the cancel")
	}
}

func TestResumeFailureKeepsTheOldSession(t *testing.T) {
	m, idA, _ := resumeTestModel(t)
	mm, cmd := m.resumeSession("missing-session")
	m = mm.(Model)
	msg := cmd()
	m = apply(t, m, msg)
	if m.session.header.ID != idA {
		t.Fatalf("a failed load switched to %q", m.session.header.ID)
	}
	if !strings.Contains(strings.Join(blockTexts(m.transcript), "\n"), "resume:") {
		t.Fatal("the failed load was not reported")
	}
}

func TestResumePendingBlocksSending(t *testing.T) {
	m, _, idB := resumeTestModel(t)
	mm, _ := m.resumeSession(idB)
	m = mm.(Model)
	next, cmd := m.startPrompt("hello")
	if cmd != nil {
		t.Fatal("a prompt started while the resume was loading")
	}
	m = next.(Model)
	if !strings.Contains(strings.Join(blockTexts(m.transcript), "\n"), "Still resuming") {
		t.Fatal("the held submit was not explained")
	}
}
