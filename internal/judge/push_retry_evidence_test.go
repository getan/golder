package judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestPriorPushRetryEvidenceRequiresMatchingFailedSandboxCall(t *testing.T) {
	command := "git -C ../annotation-workbench push origin main"
	args := func(cmd string, escaped bool) json.RawMessage {
		data, err := json.Marshal(map[string]any{"command": cmd, "sandbox_permissions": map[bool]string{true: "require_escalated", false: ""}[escaped]})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	call := func(cmd string, escaped bool) agentcore.Message {
		return agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("call-1", "bash", args(cmd, escaped))}}
	}
	result := func(envelope string, failed bool) agentcore.Message {
		return agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call-1", ToolName: "bash", IsError: failed, Details: map[string]any{"envelope": envelope, "exitCode": 128}}
	}
	for _, tc := range []struct {
		name string
		msgs agentcore.MessageList
		want bool
	}{
		{"matched", agentcore.MessageList{call(command, false), result("sandbox", true)}, true},
		{"different command", agentcore.MessageList{call("git push origin other", false), result("sandbox", true)}, false},
		{"bare failure", agentcore.MessageList{call(command, false), result("bare", true)}, false},
		{"prior escape", agentcore.MessageList{call(command, true), result("sandbox", true)}, false},
		{"success", agentcore.MessageList{call(command, false), result("sandbox", false)}, false},
		{"unpaired", agentcore.MessageList{result("sandbox", true)}, false},
		{"missing exit status", agentcore.MessageList{call(command, false), agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call-1", ToolName: "bash", IsError: true, Details: map[string]any{"envelope": "sandbox"}}}, false},
		{"new user turn", agentcore.MessageList{call(command, false), result("sandbox", true), agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("再试一次")}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := priorPushRetryEvidence("bash", args(command, true), t.TempDir(), tc.msgs)
			if (got != "") != tc.want {
				t.Fatalf("evidence=%q, want present=%t", got, tc.want)
			}
			if got != "" && !strings.Contains(got, "not a grant") {
				t.Fatalf("evidence must not authorize an escape: %q", got)
			}
		})
	}
}

func TestContainedPushRetryDoesNotRepeatTheSandboxedPush(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "https://example.com/team/repo.git")
	previous := json.RawMessage(`{"command":"git push origin main","network":"on"}`)
	current := json.RawMessage(`{"command":"git push origin main","network":"on","sandbox_permissions":"require_escalated","justification":"retry outside"}`)
	msgs := agentcore.MessageList{
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewToolCallContent("old", "bash", previous)}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolName: "bash", ToolCallID: "old", IsError: true, Details: map[string]any{"envelope": "sandbox", "exitCode": 128}},
	}
	ctx := agentcore.WithMessageSnapshot(context.Background(), msgs)
	reviewer := NewLLMJudge(nil, repo, true)
	state := reviewer.reviewState(ctx, "bash", current, msgs)
	if !strings.Contains(state, "prior matching bash call: failed inside the sandbox") {
		t.Fatalf("reviewer did not receive the structured retry evidence: %s", state)
	}
	gate := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto), Cwd: repo,
		Classifier: consentClassifier{v: Verdict{Level: Contain, Network: true}},
		Sandboxed:  func(string) bool { return true },
	})
	decision := gate(ctx, agentcore.AgentToolCall{ID: "retry", Name: "bash", Arguments: current})
	if decision == nil || !decision.Block {
		t.Fatalf("denied outside retry must not rerun in sandbox: %+v", decision)
	}
	approved := ConsentGate(ConsentOpts{
		Mode: permissions.New(permissions.Auto), Cwd: repo,
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true}},
		Sandboxed:  func(string) bool { return true },
	})
	decision = approved(ctx, agentcore.AgentToolCall{ID: "retry-approved", Name: "bash", Arguments: current})
	if decision == nil || decision.Block || !decision.Direct || !decision.Network {
		t.Fatalf("approved retry must run outside with network: %+v", decision)
	}
}
