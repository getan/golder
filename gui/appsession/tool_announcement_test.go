package appsession

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

type partialToolProvider struct {
	started chan struct{}
}

func (*partialToolProvider) Name() string { return "partial-tool" }
func (*partialToolProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "partial-tool", ID: "test"}}
}
func (p *partialToolProvider) StreamCompletion(ctx context.Context, _ provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	stream := provider.NewAssistantMessageEventStream(1)
	go func() {
		defer stream.Close()
		partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant,
			Content: agentcore.ContentList{agentcore.NewToolCallContent("partial-call", "bash", json.RawMessage(`{"command":"unredacted-partial-value"}`))}}
		if err := stream.Emit(ctx, provider.StreamToolCallEvent{Partial: partial}); err != nil {
			stream.SetError(err)
			return
		}
		close(p.started)
		<-ctx.Done()
		stream.SetError(ctx.Err())
	}()
	return stream, nil
}

func TestAnnouncedToolHidesPartialArgumentsAndClosesOnInterrupt(t *testing.T) {
	c, _, _ := newTestController(t)
	p := &partialToolProvider{started: make(chan struct{})}
	c.mu.Lock()
	c.live.Provider, c.opt.Provider = p, p
	c.mu.Unlock()
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not announce the tool")
	}
	waitSteeringEvent(t, c, EventItemAdded, "bash")
	var before *ToolState
	for _, item := range c.Snapshot().Items {
		if item.Kind == ItemTool && item.Tool.ID == "partial-call" {
			before = item.Tool
		}
	}
	if before == nil || !before.Running || len(before.Input) != 0 {
		t.Fatalf("partial tool arguments became visible: %+v", before)
	}
	c.Cancel()
	waitSteeringEvent(t, c, EventRunEnd, "")
	for _, item := range c.Snapshot().Items {
		if item.Kind == ItemTool && item.Tool.ID == "partial-call" {
			if item.Tool.Running || !item.Tool.Error || !item.Done || !item.Failed {
				t.Fatalf("interrupted tool stayed open: %+v", item)
			}
			return
		}
	}
	t.Fatal("interrupted tool card disappeared")
}
