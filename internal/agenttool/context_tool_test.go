package agenttool

import (
	"context"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/contextbudget"
)

// toolText extracts the text of a single-content tool result.
func toolText(t *testing.T, res agentcore.AgentToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("result content = %+v, want one part", res.Content)
	}
	return agentcore.ContentToText(res.Content)
}

func TestGetContextRemainingReportsBudget(t *testing.T) {
	state := contextbudget.New()
	state.SetWindow(100000)
	state.Observe(75000)
	ctx := contextbudget.WithState(context.Background(), state)

	res, err := (&GetContextRemainingTool{}).Execute(ctx, "c1", nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "25000") || !strings.Contains(text, "75000") || !strings.Contains(text, "100000") {
		t.Fatalf("result = %q, want remaining/used/window figures", text)
	}
}

func TestGetContextRemainingDegradesWithoutState(t *testing.T) {
	res, err := (&GetContextRemainingTool{}).Execute(context.Background(), "c1", nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := toolText(t, res); !strings.Contains(text, "unavailable") {
		t.Fatalf("result = %q, want an unavailable note", text)
	}
}

func TestNewContextRequestsRollover(t *testing.T) {
	state := contextbudget.New()
	state.SetWindow(100000)
	state.Observe(99000)
	state.SetRolloverEnabled(true)
	ctx := contextbudget.WithState(context.Background(), state)

	res, err := (&NewContextTool{}).Execute(ctx, "c1", nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := toolText(t, res); !strings.Contains(text, "fresh context window will start") {
		t.Fatalf("result = %q, want the confirmation text", text)
	}
	if !state.TakeRolloverRequest() {
		t.Fatal("new_context must record a rollover request")
	}
}

func TestNewContextRefusesWhenRolloverDisabled(t *testing.T) {
	state := contextbudget.New()
	ctx := contextbudget.WithState(context.Background(), state)

	res, err := (&NewContextTool{}).Execute(ctx, "c1", nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := toolText(t, res); !strings.Contains(text, "not available") {
		t.Fatalf("result = %q, want the refusal text", text)
	}
	if state.TakeRolloverRequest() {
		t.Fatal("no request should be recorded when rollover is disabled")
	}
}

func TestNewContextDegradesWithoutState(t *testing.T) {
	res, err := (&NewContextTool{}).Execute(context.Background(), "c1", nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if text := toolText(t, res); !strings.Contains(text, "unavailable") {
		t.Fatalf("result = %q, want an unavailable note", text)
	}
}
