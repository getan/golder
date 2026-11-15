package androidagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	gruntime "github.com/getan/golder/internal/runtime"
)

// TestAndroidFallbackAgentLoop uses a scripted model and a gate that authorizes
// only the exact fixture calls. It makes no network requests and must not be
// treated as a test of the production auto reviewer or App lifecycle.
func TestAndroidFallbackAgentLoop(t *testing.T) {
	root := t.TempDir()
	const before = "package sample\n\nfunc greeting() string { return \"old\" }\n"
	const after = "package sample\n\nfunc greeting() string { return \"new\" }\n"
	const patch = "*** Begin Patch\n*** Update File: sample.go\n@@\n-func greeting() string { return \"old\" }\n+func greeting() string { return \"new\" }\n*** End Patch\n"
	const grep = "/system/bin/grep -n greeting sample.go"
	const find = "/system/bin/find . -name sample.go"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	gateCalls := 0
	gate := func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		gateCalls++
		var args struct {
			Path    string `json:"path"`
			Patch   string `json:"patch"`
			Command string `json:"command"`
		}
		if json.Unmarshal(call.Arguments, &args) != nil {
			return &agentcore.BeforeToolCallDecision{Block: true}
		}
		switch {
		case call.Name == "read" && args.Path == "sample.go":
			return nil
		case call.Name == "apply_patch" && args.Patch == patch:
			return nil
		case call.Name == "bash" && (args.Command == grep || args.Command == find):
			return &agentcore.BeforeToolCallDecision{Bare: true, BareReason: "test fixture only"}
		default:
			return &agentcore.BeforeToolCallDecision{Block: true}
		}
	}
	tools, err := New(Config{Root: root, BeforeToolCall: gate})
	if err != nil {
		t.Fatal(err)
	}
	defer tools.Close()
	if tools.Capabilities.Ripgrep != "" {
		t.Skip("system-search fallback requires an environment without rg")
	}
	if tools.Capabilities.Shell == "" || tools.Capabilities.Grep == "" || tools.Capabilities.Find == "" {
		t.Skip("system shell, grep or find is unavailable")
	}
	if hasTool(tools, "grep") || hasTool(tools, "find") || !hasTool(tools, "bash") {
		t.Fatalf("unexpected fallback catalog: %+v", tools.Capabilities)
	}
	if tools.Permissions.Mode() != permissions.Auto {
		t.Fatalf("permission mode = %v, want auto", tools.Permissions.Mode())
	}
	patchArgs, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	grepArgs, err := json.Marshal(map[string]any{"command": grep, "yield_time_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	findArgs, err := json.Marshal(map[string]any{"command": find, "yield_time_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		id, name string
		args     json.RawMessage
	}{
		{"before", "read", json.RawMessage(`{"path":"sample.go"}`)},
		{"grep", "bash", grepArgs},
		{"find", "bash", findArgs},
		{"patch", "apply_patch", patchArgs},
		{"after", "read", json.RawMessage(`{"path":"sample.go"}`)},
	}
	turns := 0
	modelSawCatalog := false
	stream := func(ctx context.Context, _ string, llm provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		if turns == 0 {
			var bash, dedicated bool
			for _, tool := range llm.Tools {
				bash = bash || tool.Name() == "bash"
				dedicated = dedicated || tool.Name() == "grep" || tool.Name() == "find"
			}
			modelSawCatalog = bash && !dedicated && strings.Contains(llm.SystemPrompt, "/system/bin/grep")
		}
		msg := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
		if turns < len(steps) {
			step := steps[turns]
			msg.StopReason = agentcore.StopReasonToolUse
			msg.Content = agentcore.ContentList{agentcore.NewToolCallContent(step.id, step.name, step.args)}
		} else {
			msg.StopReason = agentcore.StopReasonEndTurn
			msg.Content = agentcore.ContentList{agentcore.NewTextContent("done")}
		}
		turns++
		s := provider.NewAssistantMessageEventStream(0)
		go func() {
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg})
			s.Close()
		}()
		return s, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := gruntime.RunConfig{LoopConfig: gruntime.LoopConfig{Model: "fake", Stream: stream}, Batch: tools.Batch}
	state := &agentcore.AgentContext{SystemPrompt: tools.Prompt(), Tools: tools.Tools, Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}
	run := gruntime.StartRun(ctx, state, cfg)
	for range run.Events() {
	}
	msgs, err := run.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !modelSawCatalog || turns != len(steps)+1 || gateCalls != len(steps) {
		t.Fatalf("catalog=%t turns=%d gate calls=%d", modelSawCatalog, turns, gateCalls)
	}
	got := make(map[string]agentcore.ToolResultMessage)
	for _, msg := range msgs {
		if result, ok := msg.(agentcore.ToolResultMessage); ok {
			got[result.ToolCallID] = result
		}
	}
	for _, tc := range []struct{ id, want string }{
		{"before", `return "old"`},
		{"grep", "greeting"},
		{"find", "sample.go"},
		{"patch", "Applied 1 change"},
		{"after", `return "new"`},
	} {
		r, ok := got[tc.id]
		if !ok || r.IsError || !strings.Contains(agentcore.ContentToText(r.Content), tc.want) {
			t.Errorf("%s result = %+v, want %q", tc.id, r, tc.want)
		}
	}
	file, err := os.ReadFile(filepath.Join(root, "sample.go"))
	if err != nil || string(file) != after {
		t.Fatalf("persisted fixture = %q, %v", file, err)
	}
	denied, _ := agenttool.ExecuteToolCalls(ctx, tools.Batch, []agentcore.AgentToolCall{{
		ID: "reject", Name: "bash", Arguments: json.RawMessage(`{"command":"printf should_not_run"}`),
	}}, nil)
	if len(denied) != 1 || !denied[0].IsError {
		t.Fatalf("unapproved command was not blocked: %+v", denied)
	}
}
