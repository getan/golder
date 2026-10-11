package runtime

// Tests for a sub-agent inheriting the dispatching session's gates: the tool
// executor publishes the chain onto the tool call's context (run.Gates.Install),
// and the sub-agent tool hands it to the child. Before this, each dispatch site
// assembled the child's gates itself, so a child could end up with fewer gates
// than the run that spawned it — the exact drift these tests pin shut.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// blockingChain is a gate chain that refuses every call and reports the
// authorizations a process-isolated child would be seeded with.
func blockingChain() *agentcore.ToolGateChain {
	return &agentcore.ToolGateChain{
		BeforeToolCall: func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
			return &agentcore.BeforeToolCallDecision{
				Block:   true,
				Content: &agentcore.ContentList{agentcore.NewTextContent("blocked by the session's gates")},
			}
		},
		Snapshot: func() agentcore.ToolAuth {
			return agentcore.ToolAuth{SessionTrust: true, SessionReads: true, ReadRoots: []string{"/allowed/root"}}
		},
	}
}

// TestSubAgentInheritsChildStopHook pins the other inherited seam: a chain's
// OnChildStop is consulted when the child is about to settle, so the hook event
// written for children reaches the child the session dispatched — and the
// session's own Stop seam (which the child's factory may have installed) still
// takes precedence when it blocks.
func TestSubAgentInheritsChildStopHook(t *testing.T) {
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child done")},
	}
	chain := blockingChain()
	blocks := 0
	chain.OnChildStop = func(context.Context, *agentcore.AgentContext) (bool, string) {
		blocks++
		// Block the first settle, then let it end, so the child finishes.
		if blocks == 1 {
			return true, "not done yet"
		}
		return false, ""
	}

	sub := NewSubAgentTool(SubAgentSpec{
		Name:         "task",
		NewRunConfig: func() RunConfig { return newFauxRunCfg(child) },
	})
	ctx := agentcore.WithToolGates(context.Background(), chain)
	if _, err := sub.Execute(ctx, "id", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if blocks < 2 {
		t.Errorf("OnChildStop consulted %d time(s), want the child to settle twice (blocked once)", blocks)
	}
}

// TestSubAgentInheritsParentGates drives the goroutine path end to end: the
// child's factory wires NO gates of its own, yet a call the session's chain
// blocks must not reach the child's tool. The control case (no chain on the
// context) shows the same tool does run, so the assertion is about inheritance
// rather than about a tool that never worked.
func TestSubAgentInheritsParentGates(t *testing.T) {
	runChild := func(t *testing.T, ctx context.Context) (ran bool, text string) {
		t.Helper()
		child := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "faux-child", ID: "child"}},
			turns: []fauxTurn{
				toolCallTurn("call-1", "probe", `{}`),
				textTurn("child finished"),
			},
		}
		probe := execTool{
			name: "probe",
			mode: agentcore.ToolExecutionParallel,
			run: func(context.Context, string, json.RawMessage, agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
				ran = true
				return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("probe ran")}}, nil
			},
		}
		var sawInherited bool
		sub := NewSubAgentTool(SubAgentSpec{
			Name: "task",
			NewRunConfig: func() RunConfig {
				return newFauxRunCfg(child, probe)
			},
		})
		res, err := sub.Execute(ctx, "id", json.RawMessage(`{"prompt":"go"}`), nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		sawInherited = ran
		return sawInherited, agentcore.ContentToText(res.Content)
	}

	t.Run("inherits", func(t *testing.T) {
		ctx := agentcore.WithToolGates(context.Background(), blockingChain())
		ran, text := runChild(t, ctx)
		if ran {
			t.Fatal("the child ran a call the session's gate blocked")
		}
		if !strings.Contains(text, "child finished") {
			t.Errorf("child text = %q, want the child to settle normally", text)
		}
	})

	t.Run("no chain runs", func(t *testing.T) {
		ran, _ := runChild(t, context.Background())
		if !ran {
			t.Fatal("without a published chain the child's own registry must still work")
		}
	})
}

// TestSubAgentProcessCarriesAuthorizationSnapshot covers the other side of the
// boundary: a process-isolated child cannot share the parent's chain, so the
// request carries the decisions it would have applied. A parent with no chain
// sends nothing, and the child then fails closed on its own.
func TestSubAgentProcessCarriesAuthorizationSnapshot(t *testing.T) {
	newSub := func(t *testing.T, got *SubAgentRunParams) *SubAgentTool {
		t.Helper()
		sub := NewSubAgentTool(SubAgentSpec{
			Name:      "proc",
			Isolation: SubAgentIsolationProcess,
			Process:   SubAgentProcessConfig{Model: "faux"},
		})
		sub.processCall = func(_ context.Context, _ SubAgentProcessConfig, params SubAgentRunParams) (string, error) {
			*got = params
			return "ok", nil
		}
		return sub
	}

	t.Run("chain present", func(t *testing.T) {
		var got SubAgentRunParams
		sub := newSub(t, &got)
		ctx := agentcore.WithToolGates(context.Background(), blockingChain())
		if _, err := sub.Execute(ctx, "id", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Auth == nil {
			t.Fatal("the request must carry the session's authorization snapshot")
		}
		if !got.Auth.SessionTrust || !got.Auth.SessionReads {
			t.Errorf("snapshot = %+v, want both session grants", got.Auth)
		}
		if len(got.Auth.ReadRoots) != 1 || got.Auth.ReadRoots[0] != "/allowed/root" {
			t.Errorf("snapshot read roots = %v, want the session's grant", got.Auth.ReadRoots)
		}
	})

	t.Run("no chain omits auth", func(t *testing.T) {
		var got SubAgentRunParams
		sub := newSub(t, &got)
		if _, err := sub.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Auth != nil {
			t.Errorf("a run with no gates must not invent authorizations, got %+v", got.Auth)
		}
	})
}
