package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/provider"
)

func TestTaskForkUsesLiveInferenceConfig(t *testing.T) {
	parentBudget := contextbudget.New()
	var childBudget *contextbudget.State
	var childModel string
	var childConfig provider.StreamConfig
	var childWindow int
	var childRollover bool
	parentTurn := 0
	live := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		if agentcore.OriginFromContext(ctx) == "task" {
			childModel, childConfig = model, cfg
			childBudget = contextbudget.FromContext(ctx)
			_, childWindow, _ = childBudget.Usage()
			childRollover = childBudget.RolloverEnabled()
			return summaryStream("fork done")(ctx, model, llm, cfg)
		}
		parentTurn++
		if parentTurn == 1 {
			return scriptedStream([]agentcore.AssistantMessage{{
				RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonToolUse,
				Content: agentcore.ContentList{agentcore.NewToolCallContent("fork", "task", json.RawMessage(`{"prompt":"continue","context":"fork"}`))},
			}})(ctx, model, llm, cfg)
		}
		return summaryStream("parent done")(ctx, model, llm, cfg)
	}
	oldCalls := 0
	task := NewTaskTool(func() RunConfig {
		child := newRunCfg(func(context.Context, string, provider.LlmContext, provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
			oldCalls++
			return nil, errors.New("the launch-time provider must not be used")
		})
		// A factory must not make the fork share mutable budget state either.
		child.Budget = parentBudget
		return child
	}, nil)
	cfg := newRunCfg(live, task)
	cfg.Model, cfg.Provider = "live-model", "live-provider"
	cfg.APIKey = "static-test-key"
	cfg.GetAPIKey = func(_ context.Context, providerName string) string {
		if providerName != "live-provider" {
			return "wrong-provider"
		}
		return "live-test-key"
	}
	cfg.ThinkingLevel = agentcore.ThinkingHigh
	cfg.ContextWindow = 200_000
	cfg.Compaction = compaction.DefaultCompactionSettings
	cfg.Budget = parentBudget
	cfg.SessionID = "parent-session"
	cfg.Extra = map[string]any{"test-option": "live-value"}
	agentCtx := &agentcore.AgentContext{SystemPrompt: "live instructions", Messages: bigUserMessages(1, 40)}
	collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))
	if oldCalls != 0 || childModel != cfg.Model {
		t.Fatalf("fork model=%q launch-provider calls=%d", childModel, oldCalls)
	}
	if childConfig.APIKey != "live-test-key" || childConfig.ThinkingLevel != cfg.ThinkingLevel || childConfig.Extra["test-option"] != "live-value" {
		t.Fatalf("fork did not inherit authentication, effort and request options: %+v", childConfig)
	}
	if childWindow != cfg.ContextWindow || !childRollover {
		t.Fatalf("fork window=%d rollover=%v, want %d / true", childWindow, childRollover, cfg.ContextWindow)
	}
	if childBudget == parentBudget {
		t.Fatal("fork reset/used its parent's budget object")
	}
}

func TestTaskRunConfigSnapshotDoesNotShareParentState(t *testing.T) {
	parent := RunConfig{
		LoopConfig: LoopConfig{
			Model: "live", Provider: "provider", ContextWindow: 200_000,
			Compaction:   compaction.DefaultCompactionSettings,
			SummaryModel: provider.Model{ID: "summary"},
			Extra:        map[string]any{"option": "original"},
			TransformContext: func(context.Context, agentcore.MessageList) agentcore.MessageList {
				panic("parent reminder transform reached the child")
			},
		},
		Budget: contextbudget.New(), SessionID: "session", MemoryRoot: "/parent-memory",
		Checkpoint:    func(context.Context, *agentcore.AgentContext, bool) { panic("parent checkpoint reached child") },
		CompactionLog: func(CompactionAttempt) {},
	}
	ctx := withTaskRunConfig(context.Background(), parent)
	parent.Extra["option"] = "changed after snapshot"
	var first, second RunConfig
	inheritTaskRunConfig(ctx, &first)
	first.Extra["option"] = "first-child"
	inheritTaskRunConfig(ctx, &second)
	if second.Extra["option"] != "original" || parent.Extra["option"] != "changed after snapshot" {
		t.Fatal("request option maps are shared between the parent snapshot and siblings")
	}
	if second.TransformContext != nil || second.Checkpoint != nil || second.Budget != nil || second.Reminders != nil || second.MemoryRoot != "" || second.GetFollowUpMessages != nil {
		t.Fatal("parent-only state/callbacks leaked into the child")
	}
	if second.SummaryModel.ID != "summary" || second.Compaction != parent.Compaction || second.CompactionLog == nil || second.SessionID != parent.SessionID {
		t.Fatal("summary settings, diagnostic sink or session affinity were lost")
	}
}

func TestTaskForkCompactsInheritedHistoryBeforeFirstRequest(t *testing.T) {
	var forkFirstRole string
	parentUnchangedDuringChild := false
	parentTurn := 0
	parentCtx := &agentcore.AgentContext{Messages: bigUserMessages(12, 800)}
	live := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		if agentcore.OriginFromContext(ctx) == "task" {
			forkFirstRole = llm.Messages[0].Role()
			parentUnchangedDuringChild = parentCtx.Messages[0].Role() == agentcore.RoleUser
			return summaryStream("fork done")(ctx, model, llm, cfg)
		}
		parentTurn++
		if parentTurn == 1 {
			return scriptedStream([]agentcore.AssistantMessage{{
				RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonToolUse,
				Content: agentcore.ContentList{agentcore.NewToolCallContent("fork", "task", json.RawMessage(`{"prompt":"continue","context":"fork"}`))},
			}})(ctx, model, llm, cfg)
		}
		return summaryStream("parent done")(ctx, model, llm, cfg)
	}
	task := NewTaskTool(func() RunConfig { return newRunCfg(summaryStream("unused")) }, nil)
	cfg := newRunCfg(live, task)
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}
	cfg.SummaryStream = summaryStream("## Goal\nsummary")
	collectEvents(t, agentLoop(context.Background(), parentCtx, cfg))
	if forkFirstRole != agentcore.RoleCompaction {
		t.Fatalf("fork first request starts with %q, want compacted inherited history", forkFirstRole)
	}
	if !parentUnchangedDuringChild {
		t.Fatal("fork preflight compaction mutated its parent's history")
	}
}
