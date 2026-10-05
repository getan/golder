package runtime

// Tests for the generic task tool (US-002/003/004, #454): its identity/schema
// contract, the shared concurrency semaphore (N > cap never exceeds cap), the
// nesting guard (child tool set excludes "task"), that a task returns the
// child's final text, and that a failed child surfaces as a tool error. The
// child loop is driven through the faux provider seam (mirrors orchestration_test.go);
// only the provider boundary is faked.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

// TestTaskToolContract pins the tool identity, parallel execution mode, and the
// {description?, prompt} schema with prompt required.
func TestTaskToolContract(t *testing.T) {
	tool := NewTaskTool(func() RunConfig { return RunConfig{} }, nil)
	if tool.Name() != "task" {
		t.Errorf("Name() = %q, want task", tool.Name())
	}
	if tool.ExecutionMode() != agentcore.ToolExecutionParallel {
		t.Errorf("ExecutionMode() = %v, want parallel", tool.ExecutionMode())
	}
	// The description must tell the model how to actually get parallelism: all
	// independent task calls batched into one assistant message. This is the
	// wording that prevents the "tasks ran one after another" behavior (a task
	// dispatched in a later message waits for the previous one to return).
	if desc := strings.ToLower(tool.Description()); !strings.Contains(desc, "same assistant message") {
		t.Errorf("description should instruct batching independent calls into one message, got: %q", tool.Description())
	}
	var schema struct {
		Properties struct {
			Description json.RawMessage `json:"description"`
			Prompt      json.RawMessage `json:"prompt"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(schema.Properties.Prompt) == 0 || len(schema.Properties.Description) == 0 {
		t.Errorf("schema must declare both prompt and description properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "prompt" {
		t.Errorf("required = %v, want [prompt]", schema.Required)
	}
}

// TestTaskReturnsChildText verifies a dispatched task drives an independent child
// loop and returns the child's final assistant text as the tool result.
func TestTaskReturnsChildText(t *testing.T) {
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{textTurn("child final report")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)
	res, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"description":"do x","prompt":"do the work"}`), nil)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if got := agentcore.ContentToText(res.Content); got != "child final report" {
		t.Errorf("task result = %q, want 'child final report'", got)
	}
	if child.callCount() != 1 {
		t.Errorf("child provider calls = %d, want 1", child.callCount())
	}
}

// TestTaskFailedChildErrors verifies a child whose final turn stops on error is
// surfaced to the parent as a tool error (not a silent success).
func TestTaskFailedChildErrors(t *testing.T) {
	// A child turn ending on StopReason=error, carrying diagnostic text as content
	// (executeGoroutine surfaces the child's Content on failure).
	errTurn := func(text string) fauxTurn {
		partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
		withText := partial
		withText.Content = agentcore.ContentList{agentcore.NewTextContent(text)}
		final := withText
		final.StopReason = agentcore.StopReasonError
		return fauxTurn{
			provider.StreamStartEvent{Partial: partial},
			provider.StreamTextEvent{Partial: withText},
			provider.StreamDoneEvent{Message: final},
		}
	}
	child := &fauxProvider{
		name:   "faux-child",
		models: []provider.Model{{Provider: "faux-child", ID: "child"}},
		turns:  []fauxTurn{errTurn("child exploded")},
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFnFromProvider(child)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	tool := NewTaskTool(factory, nil)
	_, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
	if err == nil {
		t.Fatal("a child that stopped on error must surface as a tool error")
	}
	if !strings.Contains(err.Error(), "child exploded") {
		t.Errorf("error should carry the child's diagnostic, got %v", err)
	}
}

// TestTaskSemaphoreBoundsConcurrency dispatches N tasks concurrently through a
// shared semaphore of capacity cap (< N) and asserts the number of children
// running at once never exceeds cap. Each child calls a blocking fake tool that
// parks on a barrier, so all admitted children pile up simultaneously and the
// peak concurrency is observable.
func TestTaskSemaphoreBoundsConcurrency(t *testing.T) {
	const capN, n = 2, 6
	sem := make(chan struct{}, capN)

	var running, peak int64
	release := make(chan struct{})
	// blockTool parks until the test closes release, holding a semaphore slot for
	// the duration and recording the peak number of concurrent children.
	blockTool := execTool{
		name: "block",
		run: func(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
			cur := atomic.AddInt64(&running, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
					break
				}
			}
			defer atomic.AddInt64(&running, -1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("blocked")}}, nil
		},
	}
	// Each child runs one turn that calls the blocking tool, then (after release)
	// a final text turn.
	factory := func() RunConfig {
		p := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "c", ID: "c"}},
			turns:  []fauxTurn{toolCallTurn("t", "block", `{}`), textTurn("done")},
		}
		reg := agenttool.NewToolRegistry()
		_ = reg.Register(blockTool)
		return RunConfig{
			LoopConfig: LoopConfig{Model: "c", Stream: provider.StreamFnFromProvider(p)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, sem)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil)
		}()
	}
	// Give the admitted children time to reach the barrier, then let them go.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&running) < int64(capN) {
		select {
		case <-deadline:
			t.Fatalf("only %d children started, expected the semaphore to admit %d", atomic.LoadInt64(&running), capN)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// Hold briefly so any over-admission (a semaphore bug) would push peak > cap.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&peak); got > int64(capN) {
		t.Errorf("peak concurrent children = %d, must not exceed cap %d", got, capN)
	}
	if got := atomic.LoadInt64(&peak); got == 0 {
		t.Error("no child ever ran; the semaphore blocked everything")
	}
}

// TestTaskAdvertisesRegistryTools verifies the child sub-agent is told about the
// tools it can actually run: when the spec pins no explicit tool set, the child
// context's Tools are populated from the run config's registry. Without this the
// model receives an empty tool list and cannot do real work (the "non-functional
// sub-agent" bug), so this guards the wiring, not just the result.
func TestTaskAdvertisesRegistryTools(t *testing.T) {
	// Capture the tools the provider is handed for the child request.
	var gotTools []agentcore.AgentTool
	capturing := provider.StreamFn(func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		gotTools = llm.Tools
		child := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "faux-child", ID: "child"}},
			turns:  []fauxTurn{textTurn("done")},
		}
		return provider.StreamFnFromProvider(child)(ctx, model, llm, cfg)
	})
	reg := agenttool.NewToolRegistry()
	_ = reg.Register(echoTool("read", agentcore.ToolExecutionParallel, false))
	_ = reg.Register(echoTool("bash", agentcore.ToolExecutionParallel, false))
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: capturing},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg}},
		}
	}
	tool := NewTaskTool(factory, nil)
	if _, err := tool.Execute(context.Background(), "id", json.RawMessage(`{"prompt":"go"}`), nil); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if len(gotTools) != 2 {
		t.Fatalf("child was advertised %d tools, want 2 (from the registry)", len(gotTools))
	}
	names := map[string]bool{gotTools[0].Name(): true, gotTools[1].Name(): true}
	if !names["read"] || !names["bash"] {
		t.Errorf("child tools = %v, want read+bash from the registry", names)
	}
}

// multiToolCallTurn scripts one assistant turn carrying several tool calls at
// once — the shape a model emits when it dispatches multiple sub-agents in a
// single message, i.e. one parallel batch.
func multiToolCallTurn(calls ...agentcore.ToolCallContent) fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	withCalls := partial
	for _, c := range calls {
		withCalls.Content = append(withCalls.Content, c)
	}
	final := withCalls
	final.StopReason = agentcore.StopReasonToolUse
	return fauxTurn{
		provider.StreamStartEvent{Partial: partial},
		provider.StreamToolCallEvent{Partial: withCalls},
		provider.StreamDoneEvent{Message: final},
	}
}

// TestTaskCallsInOneBatchRunInParallel drives the whole loop with a single
// assistant message that carries two task calls and proves the children
// overlap: each child's stream parks on a barrier until the other arrives, so
// a serial executor times the first child out (surfacing a task error result).
// This guards the reported "tasks run one after another" regression at the
// loop→batch→subagent seam, not just in the batch executor unit test.
func TestTaskCallsInOneBatchRunInParallel(t *testing.T) {
	var arrived int64
	bothStarted := make(chan struct{})
	var once sync.Once

	barrierStream := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		n := atomic.AddInt64(&arrived, 1)
		if n == 2 {
			once.Do(func() { close(bothStarted) })
		}
		select {
		case <-bothStarted:
		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("barrier timed out at %d/2 children: task calls ran serially", n)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		child := &fauxProvider{
			name:   "faux-child",
			models: []provider.Model{{Provider: "faux-child", ID: "child"}},
			turns:  []fauxTurn{textTurn("child report")},
		}
		return provider.StreamFnFromProvider(child)(ctx, model, llm, cfg)
	}
	factory := func() RunConfig {
		return RunConfig{
			LoopConfig: LoopConfig{Model: "child", Stream: provider.StreamFn(barrierStream)},
			Batch:      agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: agenttool.NewToolRegistry()}},
		}
	}
	taskTool := NewTaskTool(factory, make(chan struct{}, 4))

	parent := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns: []fauxTurn{
			multiToolCallTurn(
				agentcore.NewToolCallContent("t1", "task", json.RawMessage(`{"description":"a","prompt":"research a"}`)),
				agentcore.NewToolCallContent("t2", "task", json.RawMessage(`{"description":"b","prompt":"research b"}`)),
			),
			textTurn("parent done"),
		},
	}
	cfg := newFauxRunCfg(parent, taskTool)
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}},
	}}

	_, msgs := collectStream(t, agentLoop(context.Background(), agentCtx, cfg))

	if got := atomic.LoadInt64(&arrived); got != 2 {
		t.Fatalf("children that reached the barrier = %d, want 2 (one was still queued behind the other)", got)
	}
	results := 0
	for _, m := range msgs {
		tr, ok := m.(agentcore.ToolResultMessage)
		if !ok {
			continue
		}
		results++
		if tr.IsError {
			t.Errorf("task %s returned an error result: %s", tr.ToolCallID, agentcore.ContentToText(tr.Content))
		}
	}
	if results != 2 {
		t.Errorf("tool results = %d, want 2 (one per task call)", results)
	}
}
