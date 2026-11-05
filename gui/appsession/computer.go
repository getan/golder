package appsession

import (
	"context"
	"errors"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/permissions"
)

// installComputerRequest advertises only a request to the local UI, never a
// model-selected window or an implicit observation/control grant.
func (c *Controller) installComputerRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.computerRequest = nil
	if c.closed || c.readOnly || c.reg == nil || c.agentCtx == nil || len(c.opt.Tools) == 0 {
		return
	}
	tool := &agenttool.ComputerRequestTool{Request: func(ctx context.Context, name string) error {
		c.mu.Lock()
		if c.closed || !c.running || c.readOnly {
			c.mu.Unlock()
			return errors.New("local operator interface unavailable")
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return err
		}
		c.seq++
		event := Event{Seq: c.seq, Session: c.header.ID, Kind: EventComputerRequest, ID: name}
		events, done := c.events, c.done
		c.mu.Unlock()
		select {
		case events <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("local operator interface unavailable")
		}
	}}
	if c.reg.Register(tool) == nil {
		c.computerRequest = tool
		c.agentCtx.Tools = append(c.agentCtx.Tools, tool)
	}
}

func (c *Controller) ResetComputerRequest() {
	c.mu.Lock()
	tool := c.computerRequest
	c.mu.Unlock()
	if tool != nil {
		tool.Reset()
	}
}

// AttachComputer is an operator-facing entry point. The session must already
// have an explicitly selected target and grants; no model-provided identifier
// can call this method. It only changes the model tool set while idle.
func (c *Controller) AttachComputer(expectedSessionID string, s *computeruse.Session) error {
	if s == nil || !s.Status().Active {
		return errors.New("computer use requires an operator-selected target")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if c.running {
		return ErrBusy
	}
	if c.readOnly {
		return ErrReadOnly
	}
	if s.Status().Capabilities.Control && c.perms != nil && c.perms.Mode() == permissions.ReadOnly {
		return errors.New("computer control is unavailable in read-only mode")
	}
	if expectedSessionID == "" || c.header.ID != expectedSessionID {
		return errors.New("computer use target selection belongs to a different conversation")
	}
	if c.agentCtx == nil || c.reg == nil || len(c.opt.Tools) == 0 {
		return errors.New("computer use is unavailable when tools are disabled")
	}
	if c.computerTool != nil {
		return errors.New("computer use is already attached")
	}
	tool := &agenttool.ComputerTool{Session: s, Mode: c.perms}
	if err := c.reg.Register(tool); err != nil {
		return err
	}
	c.agentCtx.Tools = append(c.agentCtx.Tools, tool)
	c.computerTool = tool
	return nil
}

// StopComputer revokes desktop access immediately, even during a turn. The
// tool is removed from future provider requests once the run is idle.
func (c *Controller) StopComputer() {
	c.DetachComputer(nil)
}

// DetachComputer revokes only the specified session; nil means the current
// one. This prevents a delayed UI callback from stopping a newer session.
func (c *Controller) DetachComputer(expected *computeruse.Session) {
	c.mu.Lock()
	tool := c.computerTool
	c.mu.Unlock()
	if tool == nil || (expected != nil && tool.Session != expected) {
		return
	}
	tool.Session.Stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.computerTool == tool && !c.running {
		c.removeComputerLocked()
	}
}

// removeComputerLocked is called under c.mu only when no model run owns the
// agent context. The stopped tool must never survive into a later session.
func (c *Controller) removeComputerLocked() {
	if c.computerTool == nil || c.running {
		return
	}
	if c.agentCtx != nil {
		tools := make([]agentcore.AgentTool, 0, len(c.agentCtx.Tools))
		for _, tool := range c.agentCtx.Tools {
			if tool != c.computerTool {
				tools = append(tools, tool)
			}
		}
		c.agentCtx.Tools = tools
	}
	if c.reg != nil {
		c.reg.UnregisterIf("computer", c.computerTool)
	}
	c.computerTool = nil
}
