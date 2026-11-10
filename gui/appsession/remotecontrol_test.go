package appsession

import (
	"context"
	"testing"
	"time"

	"github.com/getan/golder/internal/judge"
)

func testMobileApproval(t *testing.T) (*Controller, *MobileAdapter, chan judge.ApprovalAnswer, context.CancelFunc) {
	t.Helper()
	c := New(Options{})
	c.header.ID = "session-1"
	c.cwd = "/project"
	a, err := NewMobileAdapter(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	answer := make(chan judge.ApprovalAnswer, 1)
	go func() {
		answer <- c.confirmApproval(ctx, judge.ApprovalRequest{Kind: judge.ApprovalConsent, Tool: "bash", Summary: "write output", Needs: []judge.Need{{Cap: judge.CapWriteOutside, Path: "/outside/file", Dir: "/outside"}}})
	}()
	select {
	case event := <-c.Events():
		if event.Kind != EventApproval {
			t.Fatalf("event: %+v", event)
		}
	case <-ctx.Done():
		t.Fatal("approval was not registered")
	}
	return c, a, answer, cancel
}

func TestMobileAdapterApprovesExactPendingRequest(t *testing.T) {
	c, adapter, answers, cancel := testMobileApproval(t)
	defer cancel()
	snapshot, err := adapter.Snapshot()
	if err != nil || len(snapshot.Approvals) != 1 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	prompt := snapshot.Approvals[0]
	if len(prompt.Needs) != 1 || prompt.Needs[0].Path != "/outside/file" {
		t.Fatalf("needs: %+v", prompt.Needs)
	}
	if err := adapter.Answer(context.Background(), prompt.ID, "once"); err != nil {
		t.Fatal(err)
	}
	if err := c.RespondApproval("approval-1", AnswerOnce); err == nil {
		t.Fatal("local answer replaced the remote answer")
	}
	select {
	case answer := <-answers:
		if !answer.Answered || !answer.Approve || answer.Scope != judge.ScopeOnce || len(answer.Needs) != 1 || answer.Needs[0].Path != "/outside/file" {
			t.Fatalf("answer: %+v", answer)
		}
	case <-time.After(time.Second):
		t.Fatal("approval did not settle")
	}
}

func TestMobileAdapterDisconnectLeavesNoRememberedDenial(t *testing.T) {
	_, adapter, answers, cancel := testMobileApproval(t)
	defer cancel()
	if _, err := adapter.Snapshot(); err != nil {
		t.Fatal(err)
	}
	adapter.Disconnected()
	select {
	case answer := <-answers:
		if answer.Answered || answer.Approve {
			t.Fatalf("disconnect answer: %+v", answer)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not resolve approval")
	}
}

func TestMobileAdapterRefusesAnotherSessionAndPrivateAuthorization(t *testing.T) {
	c, adapter, _, cancel := testMobileApproval(t)
	defer cancel()
	c.mu.Lock()
	c.privateAuthorization = true
	c.mu.Unlock()
	if _, err := adapter.Snapshot(); err == nil {
		t.Fatal("private authorization was visible")
	}
	c.mu.Lock()
	c.privateAuthorization = false
	c.header.ID = "session-2"
	c.mu.Unlock()
	if _, err := adapter.Snapshot(); err == nil {
		t.Fatal("switched controller remained visible")
	}
	if err := adapter.Submit(context.Background(), "/permissions full-access"); err == nil {
		t.Fatal("remote slash command accepted")
	}
}
