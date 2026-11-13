package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/toolchain"
)

type profileAllowReviewer struct{}

func (profileAllowReviewer) Classify(context.Context, string, json.RawMessage) judge.Verdict {
	return judge.Verdict{Level: judge.Allow, Source: "test-host", Reasons: []string{"approved scoped capabilities"}}
}

func TestProfileAuthorizationTraversesTheRealExecutor(t *testing.T) {
	plan := toolchain.Plan{Name: "go", Binding: "binding", Executable: "/usr/local/go/bin/go", ReadDirs: []string{"/cache/modules"}, WriteDirs: []string{"/cache/build"}}
	resolve := func(context.Context, string, string) (toolchain.Plan, error) { return plan, nil }
	runner := &stubRunner{}
	tool := &BashTool{Dir: t.TempDir(), Sandbox: runner, Permissions: permissions.New(permissions.Ask), ResolveToolchain: resolve}
	cfg := newExecCfg(t, tool)
	gate := judge.ConsentGate(judge.ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: tool.Dir, Classifier: profileAllowReviewer{}, Sandboxed: func(string) bool { return true }, ResolveToolchain: resolve, Confirm: func(_ context.Context, request judge.ConsentRequest) judge.ConsentAnswer {
		return judge.ConsentAnswer{Allow: true, Answered: true, Scope: judge.ScopeOnce, Needs: request.Needs}
	}})
	cfg.BeforeToolCall = gate
	call := agentcore.AgentToolCall{Name: "bash", ID: "profile-executor", Arguments: json.RawMessage(`{"command":"go test ./...","profile":"go"}`)}
	msg, _ := executeToolCall(context.Background(), cfg, call, nil)
	if msg.IsError || runner.calls != 1 || runner.policy.Network || len(runner.policy.WriteDirs) != 1 {
		t.Fatalf("profile capability did not reach the execution layer: %+v %+v", msg, runner.policy)
	}
	details := msg.Details.(map[string]any)
	if details["authorization"].(agentcore.AuthorizationDecision).Mode != "ask" || details["execution_envelope"] != "sandbox" {
		t.Fatalf("execution lost its exact authorization: %+v", details)
	}
	tool.StopSessions()
}

func TestBlockedDecisionIsStructuredAndModelVisible(t *testing.T) {
	cfg := newExecCfg(t, execTool{name: "blocked", run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		t.Fatal("blocked tool executed")
		return agentcore.AgentToolResult{}, nil
	}})
	cfg.BeforeToolCall = func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Block: true, Authorization: &agentcore.AuthorizationDecision{Mode: "auto", Source: "llm", Reason: "reviewer_unavailable"}}
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{Name: "blocked", ID: "denied"}, nil)
	if !msg.IsError || !strings.Contains(agentcore.ContentToText(msg.Content), "reviewer_unavailable") || msg.Details.(map[string]any)["authorization"].(agentcore.AuthorizationDecision).Source != "llm" {
		t.Fatalf("blocked capability answer was not propagated: %+v", msg)
	}
}
