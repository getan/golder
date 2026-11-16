package mobileagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

func TestTakeoverBarrierWaitsForActiveCallAndInvalidatesPendingCall(t *testing.T) {
	p := &pauseBarrier{}
	ctx := context.Background()
	agentCtx := &agentcore.AgentContext{}
	p.beforeTurn(ctx, agentCtx)
	release, err := p.begin(ctx, agentcore.AgentToolCall{})
	if err != nil {
		t.Fatal(err)
	}
	p.pause()
	if paused, quiescent := p.status(); !paused || quiescent {
		t.Fatal("takeover became ready with an active call")
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.begin(ctx, agentcore.AgentToolCall{})
		result <- err
	}()
	release()
	if paused, quiescent := p.status(); !paused || !quiescent {
		t.Fatal("completed call did not make the pause quiescent")
	}
	select {
	case err := <-result:
		t.Fatalf("pending call escaped the pause: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	p.resume()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "not executed") {
		t.Fatalf("stale call error = %v", err)
	}
	p.beforeTurn(ctx, agentCtx)
	if len(agentCtx.Messages) != 1 {
		t.Fatalf("handback guidance missing: %+v", agentCtx.Messages)
	}
	release, err = p.begin(ctx, agentcore.AgentToolCall{})
	if err != nil {
		t.Fatalf("fresh call refused: %v", err)
	}
	release()
}

func TestTakeoverWaitHonorsCancellation(t *testing.T) {
	p := &pauseBarrier{}
	p.pause()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.begin(ctx, agentcore.AgentToolCall{}); err != context.Canceled {
		t.Fatalf("cancelled call = %v", err)
	}
}

func TestSessionTakeoverRejectsToolPlannedBeforeHandback(t *testing.T) {
	registry := agenttool.NewToolRegistry()
	if err := registry.Register(echoTool{}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	proceed := make(chan struct{})
	var requests atomic.Int32
	guidance := make(chan bool, 1)
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
					Content:    agentcore.ContentList{agentcore.NewToolCallContent("old", "echo", json.RawMessage(`{"text":"old action"}`))},
					StopReason: agentcore.StopReasonToolUse,
				}
				_ = out.Emit(ctx, provider.StreamDoneEvent{Message: msg})
				return
			}
			seen := false
			for _, msg := range llm.Messages {
				if user, ok := msg.(agentcore.UserMessage); ok && strings.Contains(agentcore.ContentToText(user.Content), "handed it back") {
					seen = true
				}
			}
			guidance <- seen
			_ = out.Emit(ctx, provider.StreamDoneEvent{Message: textMessage("done", agentcore.StopReasonEndTurn)})
		}()
		return out, nil
	}
	var executed atomic.Int32
	session, err := New(Config{
		Workspace: t.TempDir(), Model: "fake", Stream: stream,
		Batch: agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
			Registry: registry,
			BeforeToolCall: func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
				executed.Add(1)
				return nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Start("test"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := session.PauseForTakeover(); err != nil || !session.TakeoverReady() {
		t.Fatalf("pause failed or not quiescent: %v", err)
	}
	close(proceed)
	time.Sleep(50 * time.Millisecond)
	if !session.Running() || executed.Load() != 0 {
		t.Fatal("the agent ran the planned tool while taken over")
	}
	session.ResumeAfterTakeover()
	events := collectRun(t, session)
	if executed.Load() != 0 {
		t.Fatal("stale tool was executed after handback")
	}
	var stale bool
	for _, event := range events {
		if event.Type == EventToolEnd && event.ToolCallID == "old" && event.IsError && strings.Contains(event.Text, "not executed") {
			stale = true
		}
	}
	if !stale || !<-guidance {
		t.Fatalf("missing stale-call result or handback guidance: %+v", events)
	}
}

func TestSessionTakeoverHoldsNaturalCompletionAndContinues(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	var requests atomic.Int32
	guidance := make(chan bool, 1)
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
			} else {
				seen := false
				for _, msg := range llm.Messages {
					if user, ok := msg.(agentcore.UserMessage); ok && strings.Contains(agentcore.ContentToText(user.Content), "handed it back") {
						seen = true
					}
				}
				guidance <- seen
			}
			_ = out.Emit(ctx, provider.StreamDoneEvent{Message: textMessage("done", agentcore.StopReasonEndTurn)})
		}()
		return out, nil
	}
	session, err := New(Config{Workspace: t.TempDir(), Model: "fake", Stream: stream})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Start("test"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := session.PauseForTakeover(); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	time.Sleep(50 * time.Millisecond)
	if !session.Running() {
		t.Fatal("session ended while the person had control")
	}
	session.ResumeAfterTakeover()
	collectRun(t, session)
	if !<-guidance {
		t.Fatal("continuation did not include handback guidance")
	}
}
