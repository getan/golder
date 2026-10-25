package provider

import (
	"context"
	"io"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestRecoveryObservesServerCallsInTerminalSnapshot(t *testing.T) {
	fastRetryBackoff(t)
	cut := cutEvent(io.ErrUnexpectedEOF).(StreamErrorEvent)
	cut.Message.Content = agentcore.ContentList{agentcore.ToolCallContent{
		Type: "toolCall", ID: "hosted", Name: "search", Kind: agentcore.ToolCallKindServer,
	}}
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cut},
		{startEvent(), doneEvent("must not replay")},
	}}
	_ = drainEvents(t, p, context.Background())
	if p.attemptsRun() != 1 {
		t.Fatal("a provider-executed call in an error snapshot was replayed")
	}
}

func TestRecoveryDoesNotExposeToolsThroughTextSnapshots(t *testing.T) {
	fastRetryBackoff(t)
	snapshot := textEvent("plan")
	e := snapshot.(StreamTextEvent)
	e.Partial.Content = append(e.Partial.Content, agentcore.ToolCallContent{Type: "toolCall", ID: "cut", Name: "bash"})
	p := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), e, cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), doneEvent("finished")},
	}}
	for _, event := range drainEvents(t, p, context.Background()) {
		if text, ok := event.(StreamTextEvent); ok && len(text.Partial.ToolCalls()) != 0 {
			t.Fatal("a text snapshot exposed an uncommitted tool call")
		}
	}
}
