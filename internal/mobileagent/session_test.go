package mobileagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

func scriptedStream(messages ...agentcore.AssistantMessage) provider.StreamFn {
	var mu sync.Mutex
	next := 0
	return func(ctx context.Context, _ string, _ provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		mu.Lock()
		i := next
		next++
		mu.Unlock()
		msg := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}
		if i < len(messages) {
			msg = messages[i]
		}
		stream := provider.NewAssistantMessageEventStream(0)
		go func() {
			_ = stream.Emit(ctx, provider.StreamDoneEvent{Message: msg})
			stream.Close()
		}()
		return stream, nil
	}
}

func textMessage(text, stop string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
		StopReason: stop,
	}
}

func collectRun(t *testing.T, session *Session) []Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var all []Event
	for {
		all = append(all, session.Poll(0)...)
		if hasRunEnd(all) {
			return all
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish; events: %+v", all)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hasRunEnd(events []Event) bool {
	for _, e := range events {
		if e.Type == EventRunEnd {
			return true
		}
	}
	return false
}

func joinedText(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Type == EventText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func TestSessionStreamsTextAndCompletes(t *testing.T) {
	session, err := New(Config{
		Workspace: t.TempDir(),
		Model:     "fake",
		Provider:  "fake",
		Stream:    scriptedStream(textMessage("hello", agentcore.StopReasonEndTurn)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()

	if err := session.Start("say hello"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectRun(t, session)
	if len(events) == 0 || events[0].Type != EventRunStart {
		t.Fatalf("first event = %+v, want run_start", events)
	}
	if got := joinedText(events); got != "hello" {
		t.Fatalf("streamed text = %q, want hello", got)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("seq not increasing: %+v", events)
		}
	}
	end := events[len(events)-1]
	if end.Type != EventRunEnd || end.StopReason != agentcore.StopReasonEndTurn || end.Error != "" || end.Cancelled {
		t.Fatalf("run_end = %+v", end)
	}
	if session.Running() {
		t.Fatal("session still running after run_end")
	}
	snap := session.Snapshot()
	if snap.Running || len(snap.Messages) != 2 {
		t.Fatalf("snapshot = %+v, want idle with user+assistant", snap)
	}
	raw, err := session.SnapshotJSON()
	if err != nil || !strings.Contains(raw, `"messages"`) {
		t.Fatalf("SnapshotJSON = %q, %v", raw, err)
	}

	// A completed run must not lock the session: the same conversation accepts
	// the next prompt.
	if err := session.Start("again"); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	collectRun(t, session)
}

// TestSnapshotDuringRun exercises the consistency contract: Snapshot must not
// read the loop-owned message slice while the loop mutates it. Checkpoints
// publish copies; this test fails under -race if that boundary regresses.
func TestSnapshotDuringRun(t *testing.T) {
	const updates = 40
	stream := func(ctx context.Context, _ string, _ provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		s := provider.NewAssistantMessageEventStream(0)
		go func() {
			for i := 1; i <= updates; i++ {
				partial := agentcore.AssistantMessage{
					RoleField: agentcore.RoleAssistant,
					Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", i))},
				}
				if err := s.Emit(ctx, provider.StreamTextEvent{Partial: partial}); err != nil {
					break
				}
			}
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: textMessage(strings.Repeat("x", updates), agentcore.StopReasonEndTurn)})
			s.Close()
		}()
		return s, nil
	}
	session, err := New(Config{Workspace: t.TempDir(), Model: "fake", Stream: stream})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()
	if err := session.Start("stream"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stop := make(chan struct{})
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	go func() {
		defer snapshots.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = session.Snapshot()
			}
		}
	}()
	events := collectRun(t, session)
	close(stop)
	snapshots.Wait()
	if got := joinedText(events); got != strings.Repeat("x", updates) {
		t.Fatalf("streamed text length = %d, want %d", len(got), updates)
	}
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo text" }
func (echoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)
}
func (echoTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (echoTool) Execute(_ context.Context, _ string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(err.Error())}, IsError: true}, nil
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("echo:" + in.Text)}}, nil
}

func TestSessionRunsToolThroughHostGate(t *testing.T) {
	registry := agenttool.NewToolRegistry()
	if err := registry.Register(echoTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var gateCalls int
	var mu sync.Mutex
	gate := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		mu.Lock()
		gateCalls++
		mu.Unlock()
		return &agentcore.BeforeToolCallDecision{Direct: true}
	}
	toolTurn := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewToolCallContent("call-1", "echo", json.RawMessage(`{"text":"hi"}`))},
		StopReason: agentcore.StopReasonToolUse,
	}
	session, err := New(Config{
		Workspace: t.TempDir(),
		Model:     "fake",
		Provider:  "fake",
		Stream:    scriptedStream(toolTurn, textMessage("done", agentcore.StopReasonEndTurn)),
		Batch: agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
			Registry: registry, BeforeToolCall: gate,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()

	if err := session.Start("use echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectRun(t, session)
	var start, end *Event
	for i := range events {
		switch events[i].Type {
		case EventToolStart:
			start = &events[i]
		case EventToolEnd:
			end = &events[i]
		}
	}
	if start == nil || end == nil {
		t.Fatalf("missing tool events: %+v", events)
	}
	if start.ToolName != "echo" || start.ToolCallID != "call-1" || !strings.Contains(string(start.Args), `"hi"`) {
		t.Fatalf("tool_start = %+v", start)
	}
	if end.Text != "echo:hi" || end.IsError {
		t.Fatalf("tool_end = %+v", end)
	}
	mu.Lock()
	calls := gateCalls
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("host gate calls = %d, want 1", calls)
	}
	if got := joinedText(events); got != "done" {
		t.Fatalf("final text = %q", got)
	}
	if msgs := session.Snapshot().Messages; len(msgs) != 4 {
		t.Fatalf("messages = %d, want user+assistant+toolResult+assistant: %+v", len(msgs), msgs)
	}
}

func TestSessionRequiresHostGateForTools(t *testing.T) {
	registry := agenttool.NewToolRegistry()
	if err := registry.Register(echoTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := New(Config{
		Workspace: t.TempDir(),
		Model:     "fake",
		Stream:    scriptedStream(),
		Batch:     agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: registry}},
	})
	if err == nil || !strings.Contains(err.Error(), "authorization gate") {
		t.Fatalf("New without gate = %v, want authorization gate error", err)
	}
}

func TestSessionCancelEndsRun(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	var calls int32
	stream := func(ctx context.Context, _ string, _ provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		s := provider.NewAssistantMessageEventStream(0)
		if atomic.AddInt32(&calls, 1) > 1 {
			go func() {
				_ = s.Emit(ctx, provider.StreamDoneEvent{Message: textMessage("recovered", agentcore.StopReasonEndTurn)})
				s.Close()
			}()
			return s, nil
		}
		go func() {
			once.Do(func() { close(started) })
			<-ctx.Done()
			s.SetError(ctx.Err())
			s.Close()
		}()
		return s, nil
	}
	session, err := New(Config{Workspace: t.TempDir(), Model: "fake", Stream: stream})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()
	if err := session.Start("wait"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider stream never started")
	}
	session.Cancel()
	events := collectRun(t, session)
	end := events[len(events)-1]
	if end.Type != EventRunEnd || !end.Cancelled {
		t.Fatalf("run_end = %+v, want cancelled", end)
	}
	if session.Running() {
		t.Fatal("session still running after cancellation")
	}
	if err := session.Start("after cancel"); err != nil {
		t.Fatalf("Start after cancel: %v", err)
	}
	collectRun(t, session)
}

func TestSessionRejectsBusyAndEmptyRuns(t *testing.T) {
	release := make(chan struct{})
	stream := func(ctx context.Context, _ string, _ provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		s := provider.NewAssistantMessageEventStream(0)
		go func() {
			select {
			case <-release:
				s.SetResult(agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn})
			case <-ctx.Done():
				s.SetError(ctx.Err())
			}
			s.Close()
		}()
		return s, nil
	}
	session, err := New(Config{Workspace: t.TempDir(), Model: "fake", Stream: stream})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()
	if err := session.Start("  "); err == nil {
		t.Fatal("empty prompt was accepted")
	}
	if err := session.Start("first"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Start("second"); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("concurrent Start = %v, want busy error", err)
	}
	close(release)
	collectRun(t, session)
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := session.Start("after close"); err == nil {
		t.Fatal("Start after Close was accepted")
	}
}

func TestPollJSONEmptyQueueIsArray(t *testing.T) {
	session, err := New(Config{Workspace: t.TempDir(), Model: "fake", Stream: scriptedStream()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer session.Close()
	raw, err := session.PollJSON(10)
	if err != nil {
		t.Fatalf("PollJSON: %v", err)
	}
	if raw != "[]" {
		t.Fatalf("PollJSON empty = %q, want []", raw)
	}
}
