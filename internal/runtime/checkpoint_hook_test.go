package runtime

// Tests for RunConfig.Checkpoint, the mid-run persistence seam: the loop must
// hand the conversation to the hook at each turn boundary (assistant finalized,
// tool results appended) and flag a call that follows an in-place compaction so
// the persister can re-save linearly instead of appending a stale tail.

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/provider"
)

// checkpointCall records one hook invocation: the context length at call time
// and whether the call followed a context rewrite.
type checkpointCall struct {
	length    int
	compacted bool
}

// TestCheckpointHookFiresAtTurnBoundaries drives a text→tool→text run and
// asserts the hook fires after the assistant message is finalized, after tool
// results are appended, and again after the next assistant message — each time
// with the conversation as of that boundary.
func TestCheckpointHookFiresAtTurnBoundaries(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			toolCallTurn("call-1", "echo", `{}`),
			textTurn("all done"),
		},
	}
	cfg := newFauxRunCfg(p, echoTool("echo", agentcore.ToolExecutionParallel, false))
	var calls []checkpointCall
	cfg.Checkpoint = func(_ context.Context, agentCtx *agentcore.AgentContext, compacted bool) {
		calls = append(calls, checkpointCall{length: len(agentCtx.Messages), compacted: compacted})
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}},
	}}

	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	// Boundaries: user+assistant(tool call) → +tool result → +final assistant.
	want := []int{2, 3, 4}
	if len(calls) != len(want) {
		t.Fatalf("checkpoint calls = %d (%+v), want %d", len(calls), calls, len(want))
	}
	for i, w := range want {
		if calls[i].length != w {
			t.Errorf("checkpoint %d saw %d messages, want %d (%+v)", i, calls[i].length, w, calls)
		}
		if calls[i].compacted {
			t.Errorf("checkpoint %d must not be flagged compacted in a run without compaction", i)
		}
	}
	if last := calls[len(calls)-1].length; last != 1+len(msgs) {
		t.Errorf("last checkpoint saw %d messages, want the full run (%d)", last, 1+len(msgs))
	}
}

// TestCheckpointHookSignalsCompactionRebase mirrors the auto-compaction trigger
// test and asserts the hook is told when the context was rewritten, so a
// persister re-saves the flattened conversation instead of appending a tail the
// branch cursor no longer describes.
func TestCheckpointHookSignalsCompactionRebase(t *testing.T) {
	main := scriptedStream([]agentcore.AssistantMessage{
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn, Content: agentcore.ContentList{agentcore.NewTextContent("ok")}},
	})
	cfg := newRunCfg(main)
	cfg.SummaryStream = summaryStream("## Goal\ncompacted")
	cfg.ContextWindow = 2000
	cfg.Compaction = compaction.CompactionSettings{Enabled: true, ReserveTokens: 500, KeepRecentTokens: 100}

	var calls []checkpointCall
	cfg.Checkpoint = func(_ context.Context, agentCtx *agentcore.AgentContext, compacted bool) {
		calls = append(calls, checkpointCall{length: len(agentCtx.Messages), compacted: compacted})
	}

	const seeded = 12
	agentCtx := &agentcore.AgentContext{Messages: bigUserMessages(seeded, 800)}
	collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if len(calls) < 2 {
		t.Fatalf("checkpoint calls = %d (%+v), want at least an assistant boundary and a rebase", len(calls), calls)
	}
	first := calls[0]
	if first.compacted || first.length != seeded+1 {
		t.Errorf("first checkpoint = %+v, want the assistant finalized on the seeded context (%d)", first, seeded+1)
	}
	last := calls[len(calls)-1]
	if !last.compacted {
		t.Fatalf("last checkpoint %+v must be flagged compacted", last)
	}
	if last.length >= seeded {
		t.Errorf("rebased checkpoint saw %d messages, want the compacted (smaller) context under %d", last.length, seeded)
	}
	// The rebase call must observe the rewritten context, not the original.
	if role := agentCtx.Messages[0].Role(); role != agentcore.RoleCompaction {
		t.Errorf("context after the rebase checkpoint starts with %q, want a compaction checkpoint", role)
	}
}
