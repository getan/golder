package appsession

import (
	"errors"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// CheckGUITerminalSession pins a terminal tool to the controller incarnation,
// conversation and project that installed it. The returned permission state is
// live: a later /permissions change applies to the next call.
func (c *Controller) CheckGUITerminalSession(sessionID, project string) (*permissions.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.opt.GUI || c.closed || c.readOnly || c.agentCtx == nil || sessionID == "" ||
		c.header.ID != sessionID || !sameProjectPath(c.cwd, project) || len(c.opt.Tools) == 0 {
		return nil, errors.New("terminal: originating GUI session is no longer available")
	}
	return c.perms, nil
}

// InstallGUITerminalTool installs a session-bound frontend tool before a turn.
// It never enables tools in a --no-tools or read-only session and never replaces
// an unrelated extension under the same name.
func (c *Controller) InstallGUITerminalTool(sessionID, project string, tool agentcore.AgentTool) (*permissions.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tool == nil || tool.Name() != "terminal" || !c.opt.GUI || c.closed || c.readOnly ||
		c.agentCtx == nil || c.reg == nil || len(c.opt.Tools) == 0 || sessionID == "" ||
		c.header.ID != sessionID || !sameProjectPath(c.cwd, project) {
		return nil, errors.New("terminal: GUI tool installation is unavailable")
	}
	if c.running {
		return nil, ErrBusy
	}
	old := c.guiTerminalTool
	if old != nil {
		c.reg.UnregisterIf("terminal", old)
		filter := func(in []agentcore.AgentTool) []agentcore.AgentTool {
			out := make([]agentcore.AgentTool, 0, len(in))
			for _, t := range in {
				if t != old {
					out = append(out, t)
				}
			}
			return out
		}
		c.opt.Tools = filter(c.opt.Tools)
		c.agentCtx.Tools = filter(c.agentCtx.Tools)
	}
	if err := c.reg.Register(tool); err != nil {
		return nil, err
	}
	c.guiTerminalTool = tool
	c.opt.Tools = append(c.opt.Tools, tool)
	c.agentCtx.Tools = append(c.agentCtx.Tools, tool)
	c.prompt.Tools = c.agentCtx.Tools
	return c.perms, nil
}
