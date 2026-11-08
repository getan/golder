package appsession

import (
	"context"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
)

func TestApprovalCarriesExactCallIdentity(t *testing.T) {
	for _, origin := range []string{"", "child agent"} {
		t.Run(origin, func(t *testing.T) {
			c := New(Options{})
			c.runID = "run-1"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = agentcore.WithToolCallID(ctx, "call-42")
			ctx = agentcore.WithAgentOrigin(ctx, origin)
			answer := make(chan judge.ApprovalAnswer, 1)
			go func() {
				answer <- c.confirmApproval(ctx, judge.ApprovalRequest{Tool: "bash", Kind: judge.ApprovalConsent})
			}()
			select {
			case event := <-c.Events():
				if event.Kind != EventApproval || event.Approval == nil || event.Approval.RunID != "run-1" || event.Approval.ToolCallID != "call-42" || event.Approval.Origin != origin {
					t.Fatalf("approval identity = %+v", event)
				}
				if err := c.RespondApproval(event.ID, AnswerOnce); err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("approval was not emitted")
			}
			select {
			case got := <-answer:
				if !got.Answered || !got.Approve {
					t.Fatalf("answer = %+v", got)
				}
			case <-ctx.Done():
				t.Fatal("approval answer did not return")
			}
		})
	}
}

func TestReviewNoteCarriesExactCallIdentity(t *testing.T) {
	c := New(Options{})
	c.runID = "run-2"
	c.addNote(judge.Note{Tool: "bash", ToolCallID: "call-3", Origin: "child", Kind: judge.NoteSandboxed, Rationale: "contained"})
	items := c.Snapshot().Items
	if len(items) != 1 || items[0].Note == nil || items[0].Note.RunID != "run-2" || items[0].Note.ToolCallID != "call-3" || items[0].Note.Origin != "child" {
		t.Fatalf("note identity = %+v", items)
	}
}
