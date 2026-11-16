package mobileagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

// TestQueuedPromptSteersRunningTurn pins the TUI behaviour on mobile: a prompt
// typed during a run is queued and delivered at the next turn boundary instead
// of being refused.
func TestQueuedPromptSteersRunningTurn(t *testing.T) {
	registry := agenttool.NewToolRegistry()
	if err := registry.Register(echoTool{}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	started := make(chan struct{})
	proceed := make(chan struct{})
	steered := make(chan bool, 1)
	stream := func(ctx context.Context, _ string, llm provider.LlmContext, _ provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		n := requests.Add(1)
		out := provider.NewAssistantMessageEventStream(0)
		go func() {
			defer out.Close()
			if n == 1 {
				close(started)
				select {
				case <-proceed:
				case <-ctx.Done():
					return
				}
				msg := agentcore.AssistantMessage{
					RoleField:  agentcore.RoleAssistant,
					Content:    agentcore.ContentList{agentcore.NewToolCallContent("c1", "echo", json.RawMessage(`{"text":"hi"}`))},
					StopReason: agentcore.StopReasonToolUse,
				}
				_ = out.Emit(ctx, provider.StreamDoneEvent{Message: msg})
				return
			}
			seen := false
			for _, m := range llm.Messages {
				if u, ok := m.(agentcore.UserMessage); ok && strings.Contains(agentcore.ContentToText(u.Content), "queued instruction") {
					seen = true
				}
			}
			steered <- seen
			_ = out.Emit(ctx, provider.StreamDoneEvent{Message: textMessage("done", agentcore.StopReasonEndTurn)})
		}()
		return out, nil
	}
	session, err := New(Config{
		Workspace: t.TempDir(), Model: "fake", Stream: stream,
		Batch: agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
			Registry:       registry,
			BeforeToolCall: func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision { return nil },
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Start("first"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := session.Start("queued instruction"); err != nil {
		t.Fatalf("queueing during a run failed: %v", err)
	}
	close(proceed)
	events := collectRun(t, session)
	if !<-steered {
		t.Fatal("the queued prompt never reached the model")
	}
	queued := false
	for _, e := range events {
		if e.Type == EventQueued && strings.Contains(e.Text, "queued instruction") {
			queued = true
		}
	}
	if !queued {
		t.Fatalf("missing queued event: %+v", events)
	}
}
