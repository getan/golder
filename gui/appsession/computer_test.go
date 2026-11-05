package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/computeruse"
)

type computerBackendFixture struct{}

func (computerBackendFixture) Validate(context.Context, computeruse.Target, bool) error { return nil }
func (computerBackendFixture) Close() error                                             { return nil }

func TestComputerAttachmentIsExplicitAndRemovedOnStop(t *testing.T) {
	c, _, _ := newTestController(t)
	s := computeruse.New(computerBackendFixture{})
	if err := c.AttachComputer(c.SessionID(), s); err == nil {
		t.Fatal("unselected session was attached")
	}
	if err := s.Start(context.Background(), computeruse.Target{ID: "one"}, computeruse.Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachComputer(c.SessionID(), s); err == nil {
		t.Fatal("computer use bypassed disabled tools")
	}
	base := &agenttool.TodoTool{Store: agenttool.NewTodoStore()}
	c.mu.Lock()
	c.opt.Tools = []agentcore.AgentTool{base}
	c.agentCtx.Tools = []agentcore.AgentTool{base}
	c.reg = agenttool.NewToolRegistry()
	if err := c.reg.Register(base); err != nil {
		c.mu.Unlock()
		t.Fatal(err)
	}
	c.mu.Unlock()
	if err := c.AttachComputer(c.SessionID(), s); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.reg.Get("computer"); !ok || len(c.agentCtx.Tools) != 2 {
		t.Fatal("computer tool was not attached to both model and executor")
	}
	c.StopComputer()
	if s.Status().Active {
		t.Fatal("stop did not revoke the target")
	}
	if _, ok := c.reg.Get("computer"); ok || len(c.agentCtx.Tools) != 1 {
		t.Fatal("stopped computer tool remained registered")
	}
}
