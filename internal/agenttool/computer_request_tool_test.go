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
