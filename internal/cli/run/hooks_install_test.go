package run

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/runtime"
)

// installHooksForTest builds the run's dispatcher and wires its seams, the
// sequence every driver performs: BuildDispatcher once per session, then
// InstallSeams per run (or Gates.Install, which does both beside the gates).
func installHooksForTest(cfg *runtime.RunConfig, set hooks.HookSet, deps HookDeps) *hooks.Dispatcher {
	d := BuildDispatcher(set, deps)
	InstallSeams(cfg, d, deps)
	return d
}

func TestInstallHooksEmptyShortCircuits(t *testing.T) {
	var cfg runtime.RunConfig
	if d := installHooksForTest(&cfg, nil, HookDeps{ProjectDir: t.TempDir()}); d != nil {
		t.Fatalf("expected nil dispatcher for empty hook set, got %v", d)
	}
	if d := installHooksForTest(&cfg, hooks.HookSet{}, HookDeps{}); d != nil {
		t.Fatalf("expected nil dispatcher for empty map, got %v", d)
	}
}

func TestInstallHooksBuildsDispatcher(t *testing.T) {
	set := hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: "true"}}}},
	}
	var cfg runtime.RunConfig
	d := installHooksForTest(&cfg, set, HookDeps{ProjectDir: t.TempDir()})
	if d == nil {
		t.Fatal("expected non-nil dispatcher for non-empty hook set")
	}
}

func TestChainAfterToolCall(t *testing.T) {
	content := agentcore.ContentList{}
	prevRes := &agentcore.AfterToolCallResult{Content: &content}
	nextRes := &agentcore.AfterToolCallResult{Content: &content}
	prev := func(context.Context, agentcore.AgentToolCall, agentcore.AgentToolResult, bool) *agentcore.AfterToolCallResult {
		return prevRes
	}
	nilNext := func(context.Context, agentcore.AgentToolCall, agentcore.AgentToolResult, bool) *agentcore.AfterToolCallResult {
		return nil
	}
	next := func(context.Context, agentcore.AgentToolCall, agentcore.AgentToolResult, bool) *agentcore.AfterToolCallResult {
		return nextRes
	}

	// next returns nil → prev's result preserved.
	if got := chainAfterToolCall(prev, nilNext)(context.Background(), agentcore.AgentToolCall{}, agentcore.AgentToolResult{}, false); got != prevRes {
		t.Fatalf("expected prev result when next is nil, got %v", got)
	}
	// next returns non-nil → next wins.
	if got := chainAfterToolCall(prev, next)(context.Background(), agentcore.AgentToolCall{}, agentcore.AgentToolResult{}, false); got != nextRes {
		t.Fatalf("expected next result to win, got %v", got)
	}
}

