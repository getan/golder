package runtime

import (
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

const (
	danglingIDRead = "call_a"
	danglingIDBash = "call_b"
)

// danglingCtx builds a context whose assistant message carries tool calls,
// with results only for the ids in answered.
func danglingCtx(answered ...string) *agentcore.AgentContext {
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hi")}},
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewTextContent("working"),
				agentcore.NewToolCallContent(danglingIDRead, "read", []byte(`{"path":"a.go"}`)),
				agentcore.NewToolCallContent(danglingIDBash, "bash", []byte(`{"command":"ls"}`)),
			},
		},
	}}
	for _, id := range answered {
		ctx.Messages = append(ctx.Messages, agentcore.ToolResultMessage{
			RoleField: agentcore.RoleToolResult, ToolCallID: id, ToolName: "read",
			Content: agentcore.ContentList{agentcore.NewTextContent("ok")},
		})
	}
	return ctx
}

// TestRepairDanglingToolCalls is the regression for "the first prompt after
// resume fails with No tool output found": an assistant call left unanswered
// by an interrupted run gets a synthetic error result, placed directly after
// its call so the provider pairing is valid.
func TestRepairDanglingToolCalls(t *testing.T) {
	ctx := danglingCtx(danglingIDRead) // the bash call is left dangling
	before := len(ctx.Messages)

	if got := repairDanglingToolCalls(ctx); got != 1 {
		t.Fatalf("repaired = %d, want 1", got)
	}
	if len(ctx.Messages) != before+1 {
		t.Fatalf("messages = %d, want %d", len(ctx.Messages), before+1)
	}
	var synthetic *agentcore.ToolResultMessage
	for _, m := range ctx.Messages {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID == danglingIDBash {
			synthetic = &tr
		}
	}
	if synthetic == nil || !synthetic.IsError {
		t.Fatalf("no synthetic result for %s in %+v", danglingIDBash, ctx.Messages)
	}
	if text := agentcore.ContentToText(synthetic.Content); !strings.Contains(text, "Re-issue") {
		t.Errorf("synthetic result should tell the model it can retry, got %q", text)
	}

	// Idempotent: a second pass finds nothing to do.
	if got := repairDanglingToolCalls(ctx); got != 0 {
		t.Fatalf("second repair = %d, want 0", got)
	}
}

// TestRepairInsertsAfterItsCall pins the placement: the result must follow the
// assistant message that made the call, not land at the end of a history that
// has since moved on.
func TestRepairInsertsAfterItsCall(t *testing.T) {
	ctx := danglingCtx()
	// A later turn follows the unanswered pair.
	ctx.Messages = append(ctx.Messages,
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("next")}})

	if got := repairDanglingToolCalls(ctx); got != 2 {
		t.Fatalf("repaired = %d, want 2", got)
	}
	// [user, assistant, result_a, result_b, user]
	if len(ctx.Messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(ctx.Messages))
	}
	if _, ok := ctx.Messages[4].(agentcore.UserMessage); !ok {
		t.Fatalf("message 4 = %T, want the later user turn to stay last", ctx.Messages[4])
	}
	for i, want := range []string{"call_a", "call_b"} {
		tr, ok := ctx.Messages[2+i].(agentcore.ToolResultMessage)
		if !ok || tr.ToolCallID != want {
			t.Fatalf("message %d = %+v, want the result for %s", 2+i, ctx.Messages[2+i], want)
		}
	}
}

// TestRepairSkipsServerCalls verifies hosted calls are left alone: they ran
// provider-side and never get a local result, so synthesizing one would break
// the very pairing this repair exists to preserve.
func TestRepairSkipsServerCalls(t *testing.T) {
	ctx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{
				agentcore.NewServerToolCallContent("ws1", "web_search", []byte(`{"query":"x"}`)),
			},
		},
	}}
	if got := repairDanglingToolCalls(ctx); got != 0 {
		t.Fatalf("repaired = %d, want 0 (server calls need no local result)", got)
	}
}
