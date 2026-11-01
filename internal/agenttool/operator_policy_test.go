package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

type dynamicOperatorTool struct{ execTool }

func (dynamicOperatorTool) RequiresOperatorConfirmationFor(raw json.RawMessage) bool {
	return string(raw) == `{"confirm":true}`
}

func TestExecutorDynamicOperatorPolicy(t *testing.T) {
	for _, approve := range []bool{false, true} {
		ran := false
		tool := dynamicOperatorTool{execTool{name: "dynamic", schema: `{"type":"object"}`, run: func(ctx context.Context, id string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			ran = true
			if !agentcore.OperatorConfirmedFor(ctx, id, "dynamic", args) {
				t.Error("exact confirmation not carried into execution")
			}
			return agentcore.AgentToolResult{}, nil
		}}}
		cfg := newExecCfg(t, tool)
		if approve {
			cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				if !call.RequireOperatorConfirmation {
					t.Error("dynamic operator policy lost")
				}
				return &agentcore.BeforeToolCallDecision{OperatorConfirmed: true}
			}
		}
		msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "one", Name: "dynamic", Arguments: json.RawMessage(`{"confirm":true}`)}, func(context.Context, agentcore.AgentEvent) error { return nil })
		if ran != approve || msg.IsError == approve {
			t.Fatalf("approve=%v ran=%v result=%+v", approve, ran, msg)
		}
	}
}
