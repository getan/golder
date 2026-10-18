package provider

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func toolCallEvent() AssistantMessageEvent {
	return StreamToolCallEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{agentcore.ToolCallContent{
			Type: "toolCall", ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{}`),
		}},
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
	if len(noticeEvents(events)) == 0 {
		t.Fatal("resume should announce itself")
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

func TestResumeRefusesAToolCallCut(t *testing.T) {
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("plan "), toolCallEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("should not run"), doneEvent("should not run")},
	}}
	events := drainEvents(t, p, context.Background())
	if got := p.attemptsRun(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (tool calls are not continued)", got)
	}
	if errorMessage(events) == "" {
		t.Fatal("a tool-call cut should surface as an error, not a continuation")
	}
}
