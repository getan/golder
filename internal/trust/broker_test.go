package trust

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

func TestBrokerTrustCannotBeBypassedAndAutoDoesNotPrompt(t *testing.T) {
	mgr, _ := newTrustManager(t)
	cwd := t.TempDir()
	gate := Gate(GateOpts{Manager: mgr, Cwd: cwd, Mode: permissions.New(permissions.Auto),
		Confirm: func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
			t.Fatal("auto broker call asked for human approval")
			return judge.ApprovalAnswer{}
		},
	})
	for _, tc := range []struct{ tool, args string }{
		{"tuios", `{"action":"new"}`}, {"tuios", `{"action":"run","command":"echo ok"}`},
		{"browser", `{"action":"open"}`}, {"browser", `{"action":"screenshot"}`},
		{"workspace", `{"action":"create","preset":"review"}`},
	} {
		call := agentcore.AgentToolCall{Name: tc.tool, Arguments: json.RawMessage(tc.args)}
		if d := gate(context.Background(), call); d == nil || !d.Block {
			t.Errorf("untrusted project allowed %s %s", tc.tool, tc.args)
		}
	}
	for _, tc := range []struct{ tool, args string }{
		{"tuios", `{"action":"capture"}`}, {"browser", `{"action":"snapshot"}`}, {"workspace", `{"action":"list"}`},
	} {
		if d := gate(context.Background(), agentcore.AgentToolCall{Name: tc.tool, Arguments: json.RawMessage(tc.args)}); d != nil {
			t.Errorf("read-only broker action was trust-gated: %+v", d)
		}
	}
	mgr.SetSessionTrust(cwd)
	if d := gate(context.Background(), agentcore.AgentToolCall{Name: "tuios", Arguments: json.RawMessage(`{"action":"new"}`)}); d != nil {
		t.Fatal("project trust was not honored")
	}
}
