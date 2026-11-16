package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestAdmissionBarrierCoversPreparationAndExecution(t *testing.T) {
	var active, released int
	tool := execTool{name: "test", run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
		if active != 1 {
			t.Fatal("tool execution ran outside admission")
		}
		return agentcore.AgentToolResult{}, nil
	}}
	cfg := newExecCfg(t, tool)
	cfg.BeginToolCall = func(context.Context, agentcore.AgentToolCall) (func(), error) {
		active++
		return func() { active--; released++ }, nil
	}
	for _, name := range []string{"test", "missing"} {
		executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{Name: name}, nil)
		if active != 0 {
			t.Fatal("admission was not released")
		}
	}
	if released != 2 {
		t.Fatalf("release count = %d", released)
	}
	cfg.BeginToolCall = func(context.Context, agentcore.AgentToolCall) (func(), error) {
		return nil, errors.New("paused")
	}
	result, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{Name: "test"}, nil)
	if !result.IsError || released != 2 {
		t.Fatalf("admission failure = %+v, releases = %d", result, released)
	}
}
