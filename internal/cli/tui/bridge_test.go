package tui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// drain collects every msg queued on ch without blocking, stopping at the first
// would-block. The bridge sends are synchronous into a buffered channel, so once
// the fake sequence has been driven the msgs are all present and this returns
// them in order.
func drain(ch chan tea.Msg) []tea.Msg {
	var out []tea.Msg
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestStreamHandlerConversion drives a synthetic event sequence directly through
// the StreamHandler the bridge builds and asserts each callback produces the
// matching tea.Msg, in order. It fakes the run entirely (no provider), exercising
// the callback→msg conversion + channel ordering in isolation.
func TestStreamHandlerConversion(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)

	// A representative sequence: two text deltas, a tool start/update/end, a
	// telemetry summary, a compaction, and a turn end.
	h.OnText("Hello ")
	h.OnText("world")
	h.OnEvent(agentcore.ToolExecutionStartEvent{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Args:       map[string]any{"path": "/tmp/x"},
	})
	h.OnEvent(agentcore.ToolExecutionUpdateEvent{
		ToolCallID:    "call-1",
		ToolName:      "read_file",
		PartialResult: agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("partial")}},
	})
	h.OnEvent(agentcore.ToolExecutionEndEvent{
		ToolCallID: "call-1",
		ToolName:   "read_file",
		Result: agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent("done")},
			Details: map[string]any{"count": 3},
		},
		IsError: false,
	})
	h.OnEvent(agentcore.TelemetryEvent{Turns: 3})
	h.OnEvent(agentcore.CompactionEvent{Reason: "threshold"})
	h.OnTurnEnd(
		agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("Hello world")}},
		[]agentcore.ToolResultMessage{{ToolCallID: "call-1"}},
	)
	// Simulate drain-done: the pump appends runEndMsg after DrainStream returns.
	ch <- runEndMsg{err: nil}

	got := drain(ch)

	if len(got) != 9 {
		t.Fatalf("expected 9 msgs, got %d: %#v", len(got), got)
	}

	if m, ok := got[0].(textDeltaMsg); !ok || m.delta != "Hello " {
		t.Errorf("msg[0] = %#v, want textDeltaMsg{delta:%q}", got[0], "Hello ")
	}
	if m, ok := got[1].(textDeltaMsg); !ok || m.delta != "world" {
		t.Errorf("msg[1] = %#v, want textDeltaMsg{delta:%q}", got[1], "world")
	}
	if m, ok := got[2].(toolStartMsg); !ok || m.id != "call-1" || m.name != "read_file" || m.input["path"] != "/tmp/x" {
		t.Errorf("msg[2] = %#v, want toolStartMsg for call-1", got[2])
	}
	if m, ok := got[3].(toolUpdateMsg); !ok || m.id != "call-1" || m.partial != "partial" {
		t.Errorf("msg[3] = %#v, want toolUpdateMsg{partial:%q}", got[3], "partial")
	}
	if m, ok := got[4].(toolEndMsg); !ok || m.id != "call-1" || !m.ok || m.result != "done" {
		t.Errorf("msg[4] = %#v, want toolEndMsg{ok:true, result:%q}", got[4], "done")
	} else if dm, ok := m.details.(map[string]any); !ok || dm["count"] != 3 {
		t.Errorf("msg[4].details = %#v, want the result's Details map", m.details)
	}
	if m, ok := got[5].(telemetryMsg); !ok || m.ev.Turns != 3 {
		t.Errorf("msg[5] = %#v, want telemetryMsg{Turns:3}", got[5])
	}
	if _, ok := got[6].(compactionMsg); !ok {
		t.Errorf("msg[6] = %#v, want compactionMsg", got[6])
	}
	if m, ok := got[7].(turnEndMsg); !ok || len(m.results) != 1 || m.results[0].ToolCallID != "call-1" {
		t.Errorf("msg[7] = %#v, want turnEndMsg with one result", got[7])
	}
	if m, ok := got[8].(runEndMsg); !ok || m.err != nil {
		t.Errorf("msg[8] = %#v, want runEndMsg{err:nil}", got[8])
	}
}

// tokenDeltas extracts the spinner-count messages from a drained batch, in
// order, so a test can assert exactly what the token estimate was fed without
// pinning the surrounding tool-announcement traffic.
func tokenDeltas(msgs []tea.Msg) []int {
	var out []int
	for _, m := range msgs {
		if d, ok := m.(tokenDeltaMsg); ok {
			out = append(out, d.chars)
		}
	}
	return out
}

// TestSpinnerTokenDeltas: reasoning text and tool-call argument JSON reach the
// spinner as growth-only deltas (streaming partials are cumulative), and a new
// message start resets the marks so the next stream counts from zero.
func TestSpinnerTokenDeltas(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)
	partial := func(think, args string) agentcore.MessageUpdateEvent {
		var content agentcore.ContentList
		if think != "" {
			content = append(content, agentcore.NewThinkingContent(think))
		}
		if args != "" {
			content = append(content, agentcore.NewToolCallContent("call-1", "bash", json.RawMessage(args)))
		}
		return agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{Content: content}}
	}

	h.OnEvent(agentcore.MessageStartEvent{})
	h.OnEvent(partial("abc", ""))            // thinking grows by 3
	h.OnEvent(partial("abcdef", ""))         // +3
	h.OnEvent(partial("abcdef", `{"a":1}`))  // +7 (args appear)
	h.OnEvent(partial("abcdef", `{"a":12}`)) // +1
	h.OnEvent(agentcore.MessageStartEvent{}) // next message: marks reset
	h.OnEvent(partial("xy", ""))             // +2 from zero

	if got, want := tokenDeltas(drain(ch)), []int{3, 3, 7, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("spinner deltas = %v, want %v", got, want)
	}
}

// TestSpinnerTurnEndFlush: a provider that only delivers the finished message
// at turn end still feeds the estimate, mirroring DrainStream's text flush.
func TestSpinnerTurnEndFlush(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)
	h.OnTurnEnd(agentcore.AssistantMessage{Content: agentcore.ContentList{
		agentcore.NewThinkingContent("hello"),
		agentcore.NewToolCallContent("c1", "bash", json.RawMessage(`{"x":1}`)),
	}}, nil)
	if got, want := tokenDeltas(drain(ch)), []int{5, 7}; !slices.Equal(got, want) {
		t.Errorf("spinner deltas = %v, want %v", got, want)
	}
}

// TestToolEndError verifies the ok flag inverts IsError.
func TestToolEndError(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)
	h.OnEvent(agentcore.ToolExecutionEndEvent{
		ToolCallID: "c",
		Result:     agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("boom")}},
		IsError:    true,
	})
	got := drain(ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(got))
	}
	m, ok := got[0].(toolEndMsg)
	if !ok || m.ok || m.result != "boom" {
		t.Errorf("got %#v, want toolEndMsg{ok:false, result:%q}", got[0], "boom")
	}
}

// TestPumpPanicDeliversRunEnd: a panic in the driver side of the pump (an
// observer callback, a conversion bug) must still deliver runEndMsg. Without
// it the model would wait forever in the running state for a terminal event
// that never comes, and Ctrl+C would have nothing left to cancel.
func TestPumpPanicDeliversRunEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamFn := func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		s := provider.NewAssistantMessageEventStream(0)
		msg := agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			StopReason: agentcore.StopReasonEndTurn,
			Content:    agentcore.ContentList{agentcore.NewTextContent("hi")},
		}
		go func() {
			_ = s.Emit(ctx, provider.StreamDoneEvent{Message: msg})
			s.Close()
		}()
		return s, nil
	}
	cfg := runtime.RunConfig{LoopConfig: runtime.LoopConfig{Model: "fake", Stream: streamFn}}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser},
	}}
	ch := newEventChan()

	pump(ctx, ch, agentCtx, cfg, func(agentcore.AgentEvent) { panic("observer boom") })

	for {
		select {
		case msg := <-ch:
			if end, ok := msg.(runEndMsg); ok {
				if end.err == nil || !strings.Contains(end.err.Error(), "panic") {
					t.Fatalf("runEndMsg err = %v, want the reported panic", end.err)
				}
				return
			}
		default:
			t.Fatal("pump returned without a runEndMsg on the channel")
		}
	}
}

// TestArgsToMap covers the object / non-object coercion of tool call args.
func TestArgsToMap(t *testing.T) {
	if m := argsToMap(map[string]any{"k": "v"}); m == nil || m["k"] != "v" {
		t.Errorf("object args: got %#v, want map with k=v", m)
	}
	if m := argsToMap("not-an-object"); m != nil {
		t.Errorf("non-object args: got %#v, want nil", m)
	}
	if m := argsToMap(nil); m != nil {
		t.Errorf("nil args: got %#v, want nil", m)
	}
	// The tool executor emits Args as json.RawMessage; the card must decode it.
	if m := argsToMap(json.RawMessage(`{"command":"ls -la"}`)); m == nil || m["command"] != "ls -la" {
		t.Errorf("raw JSON object args: got %#v, want map with command=ls -la", m)
	}
	if m := argsToMap(json.RawMessage(`"just a string"`)); m != nil {
		t.Errorf("raw JSON non-object: got %#v, want nil", m)
	}
	if m := argsToMap(json.RawMessage(nil)); m != nil {
		t.Errorf("empty raw JSON: got %#v, want nil", m)
	}
}

// TestSubAgentProgressConversion verifies a SubAgentProgressEvent maps to a
// subagentProgressMsg carrying the id (parent task tool-call id), description,
// activity, and token estimate.
func TestSubAgentProgressConversion(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)
	h.OnEvent(agentcore.SubAgentProgressEvent{
		ToolCallID:  "task-1",
		Description: "build parser",
		Activity:    "Editing",
		Tokens:      256,
	})
	got := drain(ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(got))
	}
	m, ok := got[0].(subagentProgressMsg)
	if !ok {
		t.Fatalf("got %#v, want subagentProgressMsg", got[0])
	}
	if m.id != "task-1" || m.desc != "build parser" || m.activity != "Editing" || m.tokens != 256 {
		t.Errorf("got %#v, want {id:task-1 desc:build parser activity:Editing tokens:256}", m)
	}
}

// TestWaitForEvent verifies the pump Cmd returns the next queued msg.
func TestWaitForEvent(t *testing.T) {
	ch := newEventChan()
	want := runEndMsg{err: errors.New("stop")}
	ch <- want
	cmd := waitForEvent(ch)
	if cmd == nil {
		t.Fatal("waitForEvent returned nil Cmd")
	}
	got, ok := cmd().(runEndMsg)
	if !ok || got.err == nil || got.err.Error() != "stop" {
		t.Errorf("cmd() = %#v, want runEndMsg{err:stop}", got)
	}
}

// A MessageUpdateEvent carrying a tool call must announce it exactly once no
// matter how many partials repeat it; text-only partials announce nothing.
func TestStreamHandlerAnnouncesPartialToolCallsOnce(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)

	call := agentcore.NewToolCallContent("ws-1", "web_search", json.RawMessage(`{"query":"muse docs"}`))
	partial := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("let me check"), call},
	}
	h.OnEvent(agentcore.MessageUpdateEvent{Message: partial})
	h.OnEvent(agentcore.MessageUpdateEvent{Message: partial})
	h.OnEvent(agentcore.MessageUpdateEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent("let me check more")},
	}})

	got := drain(ch)
	var announces []toolAnnounceMsg
	for _, m := range got {
		if a, ok := m.(toolAnnounceMsg); ok {
			announces = append(announces, a)
		}
	}
	if len(announces) != 1 {
		t.Fatalf("expected 1 announce, got %d: %#v", len(announces), got)
	}
	if announces[0].id != "ws-1" || announces[0].name != "web_search" {
		t.Errorf("announce = %+v, want ws-1/web_search", announces[0])
	}
	if q, _ := announces[0].input["query"].(string); q != "muse docs" {
		t.Errorf("announce input query = %q, want the partial args", announces[0].input["query"])
	}
}
