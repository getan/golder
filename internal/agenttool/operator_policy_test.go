package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
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

type reviewablePolicyTool struct{ execTool }

func (reviewablePolicyTool) RequiresPolicyApproval() bool { return true }

type policyClassifier struct{}

func (policyClassifier) Classify(context.Context, string, json.RawMessage) judge.Verdict {
	return judge.Verdict{Level: judge.Confirm, Network: true}
}

func TestExecutorAutomaticallyReviewsAllLarkAndMeegleWrites(t *testing.T) {
	for _, short := range []string{"doc_write", "message_send", "ticket_update", "ticket_transition", "sheet_write", "sheet_structure", "base_record_write", "cli_call"} {
		t.Run(short, func(t *testing.T) {
			name := "mcp__lark__" + short
			ran := false
			tool := reviewablePolicyTool{execTool{name: name, schema: `{"type":"object"}`, run: func(ctx context.Context, id string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
				ran = true
				if !agentcore.PolicyApprovedFor(ctx, id, name, args) || agentcore.OperatorConfirmedFor(ctx, id, name, args) || !agentcore.NetworkGrantedFromContext(ctx) {
					t.Error("automatic approval was not bound to the call or was mislabeled as human confirmation")
				}
				return agentcore.AgentToolResult{}, nil
			}}}
			cfg := newExecCfg(t, tool)
			cfg.BeforeToolCall = judge.ConsentGate(judge.ConsentOpts{
				Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(), Classifier: policyClassifier{},
				Confirm: func(context.Context, judge.ConsentRequest) judge.ConsentAnswer {
					t.Error("auto mode asked a person")
					return judge.ConsentAnswer{}
				},
			})
			args := json.RawMessage(`{"operation":"append","options":{"doc":"example-doc","content":"test"},"confirm":true}`)
			msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "one", Name: name, Arguments: args}, func(context.Context, agentcore.AgentEvent) error { return nil })
			if !ran || msg.IsError {
				t.Fatalf("auto-reviewed tool did not run: ran=%t result=%+v", ran, msg)
			}
		})
	}
}

func TestExecutorDoesNotInventPolicyApproval(t *testing.T) {
	ran := false
	tool := reviewablePolicyTool{execTool{name: "mcp__lark__doc_write", schema: `{"type":"object"}`, run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		ran = true
		return agentcore.AgentToolResult{}, nil
	}}}
	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "one", Name: tool.Name(), Arguments: json.RawMessage(`{"confirm":true}`)}, func(context.Context, agentcore.AgentEvent) error { return nil })
	if ran || !msg.IsError {
		t.Fatalf("unapproved call ran: ran=%t result=%+v", ran, msg)
	}
}
