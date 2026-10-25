package provider

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// toolCallEvent asks for a golder tool with complete arguments: a local call the
// runtime would execute from a successful terminal message.
func toolCallEvent() AssistantMessageEvent {
	return StreamToolCallEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{agentcore.ToolCallContent{
			Type: "toolCall", ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{}`),
		}},
	}}
}

// toolCallDeltaEvent is the same call mid-argument: the JSON is unfinished, so
// there is not even a call yet — only the deltas that will build one. It is the
// shape a cut lands on when the connection dies while the model writes a long
// argument (a file, a patch), which is what makes "discard the incomplete call"
// a requirement rather than a nicety.
func toolCallDeltaEvent() AssistantMessageEvent {
	return StreamToolCallEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{agentcore.ToolCallContent{
			Type:      "toolCall",
			ID:        "call-1",
			Name:      "write_file",
			Arguments: json.RawMessage(`{"path":"a.go","content":"package ma`),
		}},
	}}
}

// serverToolCallEvent is a call the provider ran itself (a hosted search). The
// kind is what marks it: golder never executes it, and it must never be replayed.
func serverToolCallEvent() AssistantMessageEvent {
	return StreamToolCallEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewServerToolCallContent("ws_1", "web_search", json.RawMessage(`{"query":"news"}`)),
		},
	}}
}

// doneWithCall is the terminal message of a turn that asks for a tool: text plus
// one complete local call, which is what the runtime executes.
func doneWithCall(text, id, name, args string) AssistantMessageEvent {
	return StreamDoneEvent{Message: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewTextContent(text),
			agentcore.NewToolCallContent(id, name, json.RawMessage(args)),
		},
		StopReason: agentcore.StopReasonToolUse,
	}}
}

func TestResumeContinuesTextAfterACut(t *testing.T) {
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	fastRetryBackoff(t)
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("Hello "), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("world"), doneEvent("world")},
	}}
	events := drainEvents(t, p, context.Background())
	if got := p.attemptsRun(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (original + continuation)", got)
	}
	if got := lastText(events); got != "Hello world" {
		t.Fatalf("last text = %q, want Hello world", got)
	}
	var merged bool
	for _, ev := range events {
		if e, ok := ev.(StreamTextEvent); ok && agentcore.ContentToText(e.Partial.Content) == "Hello world" {
			merged = true
		}
	}
	if !merged {
		t.Fatal("continuation text did not carry the already-shown prefix")
	}
	notices := noticeEvents(events)
	if len(notices) != 1 {
		t.Fatalf("notices = %d, want 1", len(notices))
	}
	// The notice still describes the cut, not the recovery: the cause is what the
	// waiting user needs, and the continuation is the trailing clause.
	if notices[0].Reason != StreamCutTruncated {
		t.Errorf("notice reason = %q, want %q", notices[0].Reason, StreamCutTruncated)
	}
	if !strings.Contains(notices[0].Detail, "continuing the cut answer") {
		t.Errorf("notice detail = %q, want the continuation named", notices[0].Detail)
	}
}

func TestResumeDisabledByEnv(t *testing.T) {
	t.Setenv("GOLDER_STREAM_RESUME", "0")
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("Hello "), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("world"), doneEvent("world")},
	}}
	events := drainEvents(t, p, context.Background())
	if got := p.attemptsRun(); got != 1 {
		t.Fatalf("attempts = %d, want 1 with the opt-out", got)
	}
	if msg := errorMessage(events); !strings.Contains(msg, "stream cut") {
		t.Fatalf("error = %q, want the stream-cut sentence", msg)
	}
}

func TestResumeIsOnByDefault(t *testing.T) {
	t.Setenv("GOLDER_STREAM_RESUME", "")
	fastRetryBackoff(t)
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("Hello "), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("world"), doneEvent("world")},
	}}
	events := drainEvents(t, p, context.Background())
	if got := p.attemptsRun(); got != 2 {
		t.Fatalf("attempts = %d, want 2 with the default on", got)
	}
	if got := lastText(events); got != "Hello world" {
		t.Fatalf("last text = %q, want Hello world", got)
	}
}

// A cut after text AND a local tool call is continued, not ended: the text is what
// the user read, and the call has not run, so the continuation keeps the text and
// asks the model for the rest. What it must not do is carry the call: an assistant
// tool call with no result is a history no wire accepts, so the continuation
// replays text only and the model re-emits a complete call.
func TestResumeContinuesTextAndDropsTheCutToolCall(t *testing.T) {
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	fastRetryBackoff(t)
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("plan "), toolCallEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("and do it"), doneWithCall("and do it", "call-2", "bash", `{"cmd":"ls"}`)},
	}}
	events := drainEvents(t, p, context.Background())
	if got := p.attemptsRun(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (the answer is continued)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Fatalf("failure = %q, want the continuation to have answered", got)
	}
	if got := lastText(events); got != "plan and do it" {
		t.Errorf("text = %q, want the shown prefix and the continuation", got)
	}

	// The continuation request: the text the user read, then the instruction —
	// and nothing else. Replaying the cut attempt's call would pair an assistant
	// tool call with no result.
	req := p.request(1)
	if got := len(req.Context.Messages); got != 2 {
		t.Fatalf("continuation context = %d messages, want the partial text and the instruction", got)
	}
	partial, ok := req.Context.Messages[0].(agentcore.AssistantMessage)
	if !ok {
		t.Fatalf("first continuation message = %T, want the partial answer", req.Context.Messages[0])
	}
	if got := agentcore.ContentToText(partial.Content); got != "plan " {
		t.Errorf("replayed partial = %q, want the visible text only", got)
	}
	if calls := partial.ToolCalls(); len(calls) != 0 {
		t.Errorf("replayed partial carries %d calls, want none", len(calls))
	}
	for _, m := range req.Context.Messages {
		if am, ok := m.(agentcore.AssistantMessage); ok && am.HasThinking() {
			t.Error("continuation replays a partial reasoning block, whose signature belongs to the cut response")
		}
	}

	// The caller ends up with exactly one call — the one the continuation
	// produced — and never saw the dropped one.
	if ids := toolCallIDs(events); len(ids) != 1 || ids[0] != "call-2" {
		t.Errorf("tool calls shown = %v, want exactly the continuation's call-2", ids)
	}
	if calls := terminalMessage(events).ToolCalls(); len(calls) != 1 || calls[0].ID != "call-2" {
		t.Errorf("terminal calls = %+v, want the continuation's call-2", calls)
	}
}
