package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestToolCallIdentityReachesGateAndExecutor(t *testing.T) {
	seen := make(chan string, 4)
	tool := execTool{name: "identity", run: func(ctx context.Context, id string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		seen <- "execute:" + id + ":" + agentcore.ToolCallIDFromContext(ctx) + ":" + agentcore.OriginFromContext(ctx)
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		seen <- "gate:" + call.ID + ":" + agentcore.ToolCallIDFromContext(ctx) + ":" + agentcore.OriginFromContext(ctx)
		return nil
	}
	ctx := agentcore.WithAgentOrigin(context.Background(), "child")
	ctx = agentcore.WithToolCallID(ctx, "parent-call")
	results, _ := ExecuteToolCalls(ctx, BatchConfig{ToolExecutorConfig: cfg}, []agentcore.AgentToolCall{
		{ID: "first", Name: "identity", Arguments: json.RawMessage(`{}`)},
		{ID: "second", Name: "identity", Arguments: json.RawMessage(`{}`)},
	}, nil)
	if len(results) != 2 || results[0].IsError || results[1].IsError {
		t.Fatalf("tool results = %+v", results)
	}
	got := make(map[string]bool)
	for range 4 {
		got[<-seen] = true
	}
	for _, want := range []string{"gate:first:first:child", "execute:first:first:child", "gate:second:second:child", "execute:second:second:child"} {
		if !got[want] {
			t.Fatalf("missing %q from %v", want, got)
		}
	}
}
