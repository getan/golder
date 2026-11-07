package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestComputerRequestOnlyQueuesValidName(t *testing.T) {
	var requested string
	tool := &ComputerRequestTool{Request: func(_ context.Context, name string) error { requested = name; return nil }}
	for _, name := range []string{"", "../Calculator", "Calculator\x1b[2J", "Calculator\nOther"} {
		raw, _ := json.Marshal(map[string]string{"application": name})
		result, err := tool.Execute(context.Background(), "id", raw, nil)
		if err != nil || !result.IsError || requested != "" {
			t.Fatalf("invalid name %q: result=%+v requested=%q err=%v", name, result, requested, err)
		}
	}
	result, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"application":" Calculator "}`), nil)
	if err != nil || result.IsError || requested != "Calculator" || !strings.Contains(agentcore.ContentToText(result.Content), "Application setup requested") {
		t.Fatalf("request: result=%+v requested=%q err=%v", result, requested, err)
	}
	if result.Terminate == nil || !*result.Terminate {
		t.Fatal("successful request did not end the run for the frontend handoff")
	}
	result, _ = tool.Execute(context.Background(), "id", json.RawMessage(`{"application":"Other"}`), nil)
	if !result.IsError || requested != "Calculator" {
		t.Fatal("a second request displaced the pending operator decision")
	}
	tool.Reset()
	result, _ = tool.Execute(context.Background(), "id", json.RawMessage(`{"application":"Other"}`), nil)
	if result.IsError || requested != "Other" {
		t.Fatal("resolved request did not allow a subsequent request")
	}
}

func TestComputerRequestRespectsReadOnlyAndContainment(t *testing.T) {
	called := false
	mode := permissions.New(permissions.ReadOnly)
	tool := &ComputerRequestTool{Mode: mode, Request: func(context.Context, string) error { called = true; return nil }}
	raw := json.RawMessage(`{"application":"Calculator"}`)
	result, err := tool.Execute(context.Background(), "id", raw, nil)
	if err != nil || !result.IsError || called {
		t.Fatalf("read-only request was accepted: %+v, %v", result, err)
	}
	mode.Set(permissions.Auto)
	result, err = tool.Execute(agentcore.WithSandboxRequest(context.Background()), "id", raw, nil)
	if err != nil || !result.IsError || called {
		t.Fatalf("contained request was accepted: %+v, %v", result, err)
	}
	t.Setenv("GOLDER_SANDBOX", "enforce")
	result, err = tool.Execute(context.Background(), "id", raw, nil)
	if err != nil || !result.IsError || called {
		t.Fatalf("enforced sandbox request was accepted: %+v, %v", result, err)
	}
}

func TestComputerRequestHandoffEndsMixedBatch(t *testing.T) {
	tool := &ComputerRequestTool{Request: func(context.Context, string) error { return nil }}
	reg := registerAll(t, tool, echoTool("read", agentcore.ToolExecutionParallel, false))
	calls := []agentcore.AgentToolCall{
		{ID: "request", Name: "computer_request", Arguments: json.RawMessage(`{"application":"Calculator"}`)},
		{ID: "read", Name: "read", Arguments: json.RawMessage(`{}`)},
	}
	results, terminate := ExecuteToolCalls(context.Background(), BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}, calls, nil)
	if len(results) != 2 || results[0].IsError || results[1].IsError || !terminate {
		t.Fatalf("mixed batch did not hand off: terminate=%t results=%+v", terminate, results)
	}
}

func TestRejectedComputerRequestDoesNotEndMixedBatch(t *testing.T) {
	tool := &ComputerRequestTool{Request: func(context.Context, string) error {
		t.Fatal("invalid request reached the frontend")
		return nil
	}}
	reg := registerAll(t, tool, echoTool("read", agentcore.ToolExecutionParallel, false))
	calls := []agentcore.AgentToolCall{
		{ID: "request", Name: "computer_request", Arguments: json.RawMessage(`{"application":"../Calculator"}`)},
		{ID: "read", Name: "read", Arguments: json.RawMessage(`{}`)},
	}
	results, terminate := ExecuteToolCalls(context.Background(), BatchConfig{ToolExecutorConfig: ToolExecutorConfig{Registry: reg}}, calls, nil)
	if len(results) != 2 || !results[0].IsError || terminate {
		t.Fatalf("rejected request ended run: terminate=%t results=%+v", terminate, results)
	}
}
