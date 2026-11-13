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

func TestStoppedComputerRemovedBetweenRuns(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "queued"}[queued], func(t *testing.T) {
			c, _, _ := newTestController(t)
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
			first := computeruse.New(computerBackendFixture{})
			if err := first.Start(context.Background(), computeruse.Target{ID: "first"}, computeruse.Capabilities{Observe: true}); err != nil {
				t.Fatal(err)
			}
			if err := c.AttachComputer(c.SessionID(), first); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.running, c.status.Running = true, true
			if queued {
				c.enqueueSteerLocked(agentcore.ContentList{agentcore.NewTextContent("next")}, "next")
			}
			c.mu.Unlock()
			first.Stop() // The computer tool stops its session during the run.
			nextCtx, _, _, started := c.finishRun(context.Background(), nil, nil, false)
			if started != queued || c.IsRunning() != queued {
				t.Fatalf("queued run state: started=%v running=%v", started, c.IsRunning())
			}
			if c.ComputerSession() != nil || len(c.agentCtx.Tools) != 1 {
				t.Fatal("stopped computer tool survived the run")
			}
			if _, ok := c.reg.Get("computer"); ok {
				t.Fatal("stopped computer tool remained in registry")
			}
			if queued {
				c.mu.Lock()
				c.cancel()
				c.running, c.cancel = false, nil
				c.mu.Unlock()
				_ = nextCtx
			}
			second := computeruse.New(computerBackendFixture{})
			if err := second.Start(context.Background(), computeruse.Target{ID: "second"}, computeruse.Capabilities{Observe: true}); err != nil {
				t.Fatal(err)
			}
			if err := c.AttachComputer(c.SessionID(), second); err != nil {
				t.Fatalf("replacement target rejected: %v", err)
			}
		})
	}
}

func TestComputerAttachReplacesOnlyStoppedSession(t *testing.T) {
	c, _, _ := newTestController(t)
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
	first := computeruse.New(computerBackendFixture{})
	second := computeruse.New(computerBackendFixture{})
	for _, s := range []*computeruse.Session{first, second} {
		if err := s.Start(context.Background(), computeruse.Target{ID: "window"}, computeruse.Capabilities{Observe: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AttachComputer(c.SessionID(), first); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachComputer(c.SessionID(), second); err == nil {
		t.Fatal("active session was replaced")
	}
	first.Stop()
	if err := c.AttachComputer(c.SessionID(), second); err != nil {
		t.Fatalf("stale stopped session blocked attachment: %v", err)
	}
	if c.ComputerSession() != second || len(c.agentCtx.Tools) != 2 {
		t.Fatal("replacement session was not installed exactly once")
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

func TestComputerLaunchRechecksConversationAndPermission(t *testing.T) {
	c, _, _ := newTestController(t)
	if err := c.CheckComputerLaunch("wrong-session"); err == nil {
		t.Fatal("launch crossed a conversation boundary")
	}
	if err := c.CheckComputerLaunch(c.SessionID()); err != nil {
		t.Fatalf("current conversation was rejected: %v", err)
	}
	c.perms.Set(permissions.ReadOnly)
	if err := c.CheckComputerLaunch(c.SessionID()); err == nil {
		t.Fatal("read-only mode did not revoke pending application launch")
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
