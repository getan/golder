package appsession

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/permissions"
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

func TestComputerRequestOnlyNotifiesLocalView(t *testing.T) {
	c, _, _ := newTestController(t)
	base := &agenttool.TodoTool{Store: agenttool.NewTodoStore()}
	c.mu.Lock()
	c.opt.Tools = []agentcore.AgentTool{base}
	c.agentCtx.Tools = []agentcore.AgentTool{base}
	c.reg = agenttool.NewToolRegistry()
	c.running = true
	c.mu.Unlock()
	c.installComputerRequest()
	c.mu.Lock()
	tool := c.computerRequest
	c.mu.Unlock()
	if tool == nil {
		t.Fatal("request tool was not installed")
	}
	result, err := tool.Execute(context.Background(), "request", json.RawMessage(`{"application":"Calculator"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("request failed: %+v %v", result, err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-c.Events():
			if event.Kind != EventComputerRequest {
				continue
			}
			if event.ID != "Calculator" || event.Session != c.SessionID() || c.computerTool != nil {
				t.Fatalf("request unexpectedly granted access: %+v", event)
			}
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
			return
		case <-deadline:
			t.Fatal("request did not reach the local view")
		}
	}
}

func TestComputerContinuationRejectsWrongConversation(t *testing.T) {
	c, _, _ := newTestController(t)
	desktop := computeruse.New(computerBackendFixture{})
	if err := desktop.Start(context.Background(), computeruse.Target{ID: "one"}, computeruse.Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.computerTool = &agenttool.ComputerTool{Session: desktop}
	c.mu.Unlock()
	if err := c.ContinueComputer(context.Background(), "wrong-session", desktop); err == nil {
		t.Fatal("continuation crossed a conversation boundary")
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		t.Fatal("rejected continuation started a run")
	}
}

func TestComputerControlCannotAttachInReadOnlyMode(t *testing.T) {
	c, _, _ := newTestController(t)
	desktop := computeruse.New(computerBackendFixture{})
	if err := desktop.Start(context.Background(), computeruse.Target{ID: "one"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	c.perms.Set(permissions.ReadOnly)
	if err := c.AttachComputer(c.SessionID(), desktop); err == nil {
		t.Fatal("control grant bypassed read-only mode")
	}
}
