package runtime

// Regression test for the stream-termination contract under a loop panic: the
// consumer blocks on Events() until Close, so a panic must still close the
// stream (reporting the panic as the run error) instead of hanging the run —
// and with it the TUI or REPL — forever, with no terminal event and nothing
// for Ctrl+C to cancel.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

func TestLoopPanicClosesStream(t *testing.T) {
	p := &fauxProvider{
		name:   "faux",
		models: []provider.Model{{Provider: "faux", ID: "faux"}},
		turns:  []fauxTurn{textTurn("done")},
	}
	cfg := newFauxRunCfg(p)
	cfg.GetFollowUpMessages = func(context.Context, *agentcore.AgentContext) []agentcore.AgentMessage {
		panic("boom in the follow-up hook")
	}
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}},
	}}

	stream := agentLoop(context.Background(), agentCtx, cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range stream.Events() {
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream never closed after a loop panic")
	}
	if _, err := stream.Result(context.Background()); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("run error = %v, want the reported panic", err)
	}
}
