package runtime

// Interrupt latency measurement: how long a cancelled run takes to actually
// stop, in the three states a user interrupts from — mid-stream, mid-tool, and
// waiting on an approval. This is the measurement behind the "Ctrl+C takes a
// moment before anything stops" report: the keypress only cancels a context,
// and the run ends when the loop reaches a cancellation point, so the delay is
// the distance to the next one.
//
// The numbers are asserted as loose UPPER BOUNDS rather than exact figures:
// the point is to catch a regression that makes a run stop waiting for
// something that does not honor cancellation (a multi-second tool, a stuck
// stream), not to pin scheduler jitter. A generous bound still fails loudly if
// a future change starts ignoring ctx.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// interruptBudget is the ceiling a cancellation must beat to be considered
// responsive. Everything measured here is in-process work (a channel close, a
// ctx check), so a second is already two orders of magnitude of headroom.
const interruptBudget = time.Second

// drainInBackground consumes the stream on its own goroutine and closes the
// returned channel when the run ends.
//
// The consumer MUST run concurrently with the measurement: a stream built with
// EventBuffer 0 is unbuffered, so the loop blocks on its first Emit until
// somebody reads — a measurement that waits for the run to be "deep enough to
// interrupt" before it starts reading deadlocks against the loop it is trying
// to measure. (Found the hard way: the first version of these tests waited for
// the tool to start and then drained, and hung on the loop's agent_start emit.)
func drainInBackground(stream *LoopEventStream) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream.Events() {
		}
	}()
	return done
}

// TestInterruptLatencyMidStream measures the stream case: the provider emits a
// delta every turn and the cancel lands between deltas. The transport selects
// on ctx.Done() and the loop's emit does too, so the delay is the pumping
// interval — not the rest of the reply.
func TestInterruptLatencyMidStream(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns:  []fauxTurn{longTextTurn(500)},
		delay:  2 * time.Millisecond,
	}
	cfg := newRunCfg(provider.StreamFnFromProvider(p))
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	stream := agentLoop(ctx, agentCtx, cfg)
	drained := drainInBackground(stream)
	// Let the stream get going, then cancel and time the drain.
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	cancel()
	<-drained
	elapsed := time.Since(start)
	if elapsed > interruptBudget {
		t.Errorf("mid-stream interrupt took %v, want < %v", elapsed, interruptBudget)
	}
	t.Logf("mid-stream interrupt latency: %v", elapsed)
}

// TestInterruptLatencyMidTool measures the tool case with a tool that honors
// ctx (the contract every built-in tool implements: bash kills its process
// group, rg is killed via CommandContext, webfetch/websearch carry ctx into
// their HTTP requests). The run must end at the tool's own cancellation point.
func TestInterruptLatencyMidTool(t *testing.T) {
	toolStarted := make(chan struct{})
	tool := execTool{
		name: "slow",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			close(toolStarted)
			// A well-behaved long tool: it waits on ctx and returns the moment
			// the run is cancelled, exactly like the built-ins.
			<-ctx.Done()
			return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent("cancelled")},
			}, ctx.Err()
		},
	}
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "slow"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), tool)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	stream := agentLoop(ctx, agentCtx, cfg)
	drained := drainInBackground(stream)
	<-toolStarted
	start := time.Now()
	cancel()
	<-drained
	elapsed := time.Since(start)
	if elapsed > interruptBudget {
		t.Errorf("mid-tool interrupt took %v, want < %v", elapsed, interruptBudget)
	}
	t.Logf("mid-tool interrupt latency: %v", elapsed)
}

// TestInterruptLatencyMidToolIgnoresCtx documents the bound that no amount of
// loop wiring can remove: a tool that ignores ctx keeps the run open until it
// returns. The measurement is deliberately of a bounded sleep, and the
// assertion is that the run ends ONCE THE TOOL DOES — the loop must not add a
// second wait of its own (it checks ctx before the next call and aborts the
// remaining batch).
func TestInterruptLatencyMidToolIgnoresCtx(t *testing.T) {
	const toolWork = 150 * time.Millisecond
	secondToolRan := false
	first := execTool{
		name: "stubborn",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			time.Sleep(toolWork) // ignores ctx on purpose
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("done")}}, nil
		},
	}
	second := execTool{
		name: "next",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			secondToolRan = true
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("ran")}}, nil
		},
	}
	// One assistant message carrying two calls: the second must be aborted, not
	// run, once the first returns into a cancelled ctx.
	assistant := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonToolUse,
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("c1", "stubborn", json.RawMessage(`{}`)),
			agentcore.NewToolCallContent("c2", "next", json.RawMessage(`{}`)),
		},
	}
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{assistant}), first, second)
	cfg.Batch.ForceSequential = true
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	stream := agentLoop(ctx, agentCtx, cfg)
	drained := drainInBackground(stream)
	time.Sleep(30 * time.Millisecond) // let the first call start
	start := time.Now()
	cancel()
	<-drained
	elapsed := time.Since(start)

	if secondToolRan {
		t.Error("the call after an unresponsive one must be aborted, not executed")
	}
	// The run may not finish before the tool does; it must finish promptly after.
	if elapsed > toolWork+interruptBudget {
		t.Errorf("interrupt behind a ctx-ignoring tool took %v, want < %v", elapsed, toolWork+interruptBudget)
	}
	t.Logf("interrupt latency behind a ctx-ignoring tool (work=%v): %v", toolWork, elapsed)
}

// TestInterruptLatencyMidApproval measures the gate case: a tool call parked on
// a confirmation that nobody will answer. Cancelling must resolve the wait
// (fail closed) rather than leaving the run parked forever — the dead-lock the
// approval-pump fix addressed for the UI side.
func TestInterruptLatencyMidApproval(t *testing.T) {
	gateEntered := make(chan struct{})
	cfg := newRunCfg(scriptedStream([]agentcore.AssistantMessage{
		oneToolAssistant("c1", "echo"),
		{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
	}), echoTool("echo", agentcore.ToolExecutionSequential, false))
	cfg.Batch.ToolExecutorConfig.BeforeToolCall = func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		close(gateEntered)
		// A gate with nobody to answer: it waits on the run's ctx, which is the
		// contract the real gates implement (confirmApproval, the remote bridge).
		<-ctx.Done()
		return &agentcore.BeforeToolCallDecision{Block: true}
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	stream := agentLoop(ctx, agentCtx, cfg)
	drained := drainInBackground(stream)
	<-gateEntered
	start := time.Now()
	cancel()
	<-drained
	elapsed := time.Since(start)
	if elapsed > interruptBudget {
		t.Errorf("mid-approval interrupt took %v, want < %v", elapsed, interruptBudget)
	}
	t.Logf("mid-approval interrupt latency: %v", elapsed)
}

// longTextTurn scripts one turn that emits size text deltas one character at a
// time, so a cancel has many chances to land mid-reply.
func longTextTurn(size int) fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	turn := fauxTurn{provider.StreamStartEvent{Partial: partial}}
	for i := 0; i < size; i++ {
		withText := partial
		withText.Content = agentcore.ContentList{agentcore.NewTextContent(repeatText("x", i+1))}
		turn = append(turn, provider.StreamTextEvent{Partial: withText})
	}
	final := partial
	final.Content = agentcore.ContentList{agentcore.NewTextContent(repeatText("x", size))}
	final.StopReason = agentcore.StopReasonEndTurn
	return append(turn, provider.StreamDoneEvent{Message: final})
}

// repeatText returns s repeated n times.
func repeatText(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
