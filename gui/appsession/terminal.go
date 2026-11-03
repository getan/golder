package appsession

import (
	"fmt"
	"io"
	"strings"

	"github.com/getan/golder/internal/cli/run"
)

// TTYSessions lists the interactive bash sessions backing the current tool
// set. Pipe-only commands are intentionally absent.
func (c *Controller) TTYSessions() []TTYSummary {
	c.mu.Lock()
	tools := c.opt.Tools
	c.mu.Unlock()
	manager := run.ExecSessionsFromTools(tools)
	if manager == nil {
		return nil
	}
	var out []TTYSummary
	for _, snapshot := range manager.List() {
		session, ok := manager.Get(snapshot.ID)
		if !ok || !session.IsTTY() {
			continue
		}
		out = append(out, TTYSummary{
			ID: snapshot.ID, Command: snapshot.Command,
			Running: snapshot.Status == "running", ExitCode: snapshot.ExitCode,
		})
	}
	return out
}

// AttachTTY returns a detachable connection to one existing Agent bash PTY.
// Closing it never kills the process; the bash/write_stdin lifecycle remains
// authoritative.
func (c *Controller) AttachTTY(id string) (io.ReadWriteCloser, error) {
	id = strings.TrimSpace(id)
	c.mu.Lock()
	tools := c.opt.Tools
	c.mu.Unlock()
	manager := run.ExecSessionsFromTools(tools)
	if manager == nil {
		return nil, fmt.Errorf("terminal: bash sessions are unavailable")
	}
	session, ok := manager.Get(id)
	if !ok {
		return nil, fmt.Errorf("terminal: session %q was not found", id)
	}
	return session.Subscribe()
}

// InterruptTTY sends the same first/second interrupt sequence write_stdin uses.
func (c *Controller) InterruptTTY(id string) error {
	c.mu.Lock()
	tools := c.opt.Tools
	c.mu.Unlock()
	manager := run.ExecSessionsFromTools(tools)
	if manager == nil {
		return fmt.Errorf("terminal: bash sessions are unavailable")
	}
	session, ok := manager.Get(strings.TrimSpace(id))
	if !ok {
		return fmt.Errorf("terminal: session %q was not found", id)
	}
	session.Interrupt()
	return nil
}
