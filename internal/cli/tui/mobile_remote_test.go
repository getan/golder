package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/remotecontrol"
)

func testMobileModel(t *testing.T) (Model, *mobileTUI) {
	t.Helper()
	s := newRemoteTestSession(t)
	m := NewModel(Options{})
	m.session = s
	snapshot, _ := m.mobileSnapshot()
	adapter := newMobileTUI(snapshot)
	s.remote = &remoteSession{secure: &remotecontrol.SecureServer{}, mobile: adapter}
	t.Cleanup(adapter.close)
	return m, adapter
}

func TestMobileTUIApprovesThroughOriginalDialog(t *testing.T) {
	m, adapter := testMobileModel(t)
	req := testApprovalRequest()
	m.session.perms = permissions.New(permissions.Ask)
	gateReply := make(chan judge.ApprovalAnswer, 1)
	m.openApproval(req, gateReply)
	if m.approval.remoteID == "" {
		t.Fatal("missing remote approval identity")
	}
	reply := make(chan error, 1)
	next, _ := m.Update(mobileActionMsg{adapter: adapter, action: mobileTUIAction{ctx: context.Background(), kind: "answer", approvalID: m.approval.remoteID, choice: "once", reply: reply}})
	if err := <-reply; err != nil {
		t.Fatal(err)
	}
	answer := awaitAnswer(t, gateReply)
	if !answer.Approve || !answer.Answered || len(answer.Needs) != len(req.Needs) {
		t.Fatalf("answer: %+v", answer)
	}
	if next.(Model).approval.active {
		t.Fatal("local dialog remained active")
	}
}

func TestMobileTUIRejectsLocalImageReferences(t *testing.T) {
	m, adapter := testMobileModel(t)
	m.startRunFn = func(string) (chan tea.Msg, tea.Cmd) {
		t.Fatal("remote input launched a local image read")
		return nil, nil
	}
	for _, text := range []string{"inspect @image:/private/photo.png", "![photo](/private/photo.png)", "/permissions full-access", "!rm file"} {
		reply := make(chan error, 1)
		next, _ := m.Update(mobileActionMsg{adapter: adapter, action: mobileTUIAction{ctx: context.Background(), kind: "input", text: text, reply: reply}})
		m = next.(Model)
		if err := <-reply; err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestMobileTUISnapshotDoesNotExposePairingCommands(t *testing.T) {
	m, _ := testMobileModel(t)
	m.transcript.addUser("/remote-control approve abc123")
	m.transcript.addUser("normal message")
	snapshot, _ := m.mobileSnapshot()
	for _, message := range snapshot.Messages {
		if strings.Contains(message.Text, "abc123") {
			t.Fatal("pairing confirmation leaked into remote transcript")
		}
	}
}

func TestMobileTUIRefusesInputAfterSessionChanges(t *testing.T) {
	m, adapter := testMobileModel(t)
	m.session.header.ID = "different-session"
	reply := make(chan error, 1)
	_, _ = m.handleMobileAction(mobileActionMsg{adapter: adapter, action: mobileTUIAction{ctx: context.Background(), kind: "input", text: "hello", reply: reply}})
	if err := <-reply; err == nil {
		t.Fatal("input crossed the session boundary")
	}
}
