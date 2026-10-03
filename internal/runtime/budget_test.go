package runtime

// Tests for the context-budget tooling: the loop publishes and refreshes the
// run's budget state, the budget reminder claims one notice per threshold
// crossing, and a model-issued new_context request rolls the window over at the
// next turn boundary even below the auto-compaction threshold.

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/contextbudget"
)

func TestBudgetReminderProviderFiresOncePerCrossing(t *testing.T) {
	state := contextbudget.New()
	state.SetWindow(100000)
	state.Observe(95000) // 5% remaining → notice tier
	ctx := contextbudget.WithState(context.Background(), state)

	p := &BudgetReminderProvider{}
	body, ok := p.Reminder(ctx, nil)
	if !ok {
		t.Fatal("expected a notice reminder")
	}
	if !strings.Contains(body, "5000") || !strings.Contains(body, "100000") {
		t.Fatalf("reminder = %q, want remaining/window figures", body)
	}
	if _, ok := p.Reminder(ctx, nil); ok {
		t.Fatal("the same crossing must not remind twice")
	}

	// Escalation to critical fires once more and advertises new_context only
	// when the run can honor it.
	state.Observe(99000)
	body, ok = p.Reminder(ctx, nil)
	if !ok || !strings.Contains(body, "nearly exhausted") {
		t.Fatalf("reminder = %q, ok=%v; want the critical tier", body, ok)
	}
	if strings.Contains(body, "new_context") {
		t.Fatalf("critical reminder must not advertise new_context while disabled: %q", body)
	}
	if _, ok := p.Reminder(ctx, nil); ok {
		t.Fatal("critical must fire only once per crossing")
	}

	state.SetRolloverEnabled(true)
	state.Observe(50000) // recovery resets the ladder
	state.Observe(99000)
	body, ok = p.Reminder(ctx, nil)
	if !ok || !strings.Contains(body, "new_context") {
		t.Fatalf("reminder = %q, ok=%v; want new_context guidance", body, ok)
	}
}

func TestBudgetReminderSilentWithoutState(t *testing.T) {
	if _, ok := (&BudgetReminderProvider{}).Reminder(context.Background(), nil); ok {
		t.Fatal("no reminder expected without budget state")
	}
}

func TestLoopPublishesBudgetState(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("ok")}},
	}))
	cfg.ContextWindow = 100000
	cfg.Budget = contextbudget.New()
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}},
	}}

	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	used, window, ok := cfg.Budget.Usage()
	if !ok {
		t.Fatal("loop must observe the budget")
	}
	if window != 100000 || used <= 0 {
		t.Fatalf("budget = used %d window %d, want used>0 window=100000", used, window)
	}
}

func TestNewContextRequestCompactsBelowThreshold(t *testing.T) {
	// The assistant calls new_context, then ends. Usage stays far below the
	// auto-compaction threshold, so only the requested rollover can compact.
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "new_context"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
	}), &agenttool.NewContextTool{})
	cfg.SummaryStream = summaryStream("## Goal\nrolled over")
	cfg.ContextWindow = 1000000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}
	cfg.Budget = contextbudget.New()
	// Enough history that the cut point keeps only the recent tail (a very small
	// conversation has nothing to summarize and the compaction is a no-op).
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(8, 400)}

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))
	ce := findCompaction(events)
	if ce == nil {
		t.Fatalf("expected a requested rollover compaction, got events %+v", events)
	}
	if ce.Reason != "requested" {
		t.Fatalf("compaction reason = %q, want requested", ce.Reason)
	}
	if ce.ErrorMessage != "" {
		t.Fatalf("rollover compaction failed: %s", ce.ErrorMessage)
	}
}

func TestNewContextRequestIgnoredWhenCompactionDisabled(t *testing.T) {
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "new_context"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("done")}},
	}), &agenttool.NewContextTool{})
	cfg.ContextWindow = 1000000
	cfg.Compaction = compaction.CompactionSettings{Enabled: false}
	cfg.Budget = contextbudget.New()
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(3, 100)}
	before := len(agentCtx.Messages)

	events := collectEvents(t, agentLoop(context.Background(), agentCtx, cfg))
	if ce := findCompaction(events); ce != nil {
		t.Fatalf("no compaction expected with compaction disabled, got %+v", ce)
	}
	// The call exchange appends three messages: the assistant tool call, its
	// result, and the final assistant reply.
	if len(agentCtx.Messages) != before+3 {
		t.Fatalf("context = %d messages, want %d", len(agentCtx.Messages), before+3)
	}
}
