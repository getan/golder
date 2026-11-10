package appsession

import (
	"context"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/runtime"
)

const guiTerminalGuide = "GUI terminal routing: You are running inside Golder GUI, which has its own built-in terminal panel. " +
	"When the user says 'open a terminal', '打开终端', '新建终端', or asks for an interactive shell without explicitly naming an external system terminal, " +
	"use the terminal tool, not computer_request or Computer Use. Use terminal action=new to open a built-in GUI terminal; " +
	"use list/focus to return to an existing pane you are authorized to control, and split for an additional split pane. " +
	"Do not launch Terminal.app, iTerm, Ghostty, Windows Terminal or another desktop terminal via computer_request, bash or open just to satisfy a generic terminal request. " +
	"Only choose an external terminal when the user explicitly requests that system terminal application; respect any target restrictions and report unsupported targets rather than bypassing them. " +
	"For ordinary noninteractive command execution, keep using bash; terminal is for a visible or interactive terminal. " +
	"Existing pane read/input grants, sandbox/network limits and PTY-denial rules still apply. If built-in terminal creation is denied or unavailable, " +
	"report the limitation or use a suitable noninteractive alternative; do not silently fall back to an external desktop terminal."

// Apply frontend guidance after extension catalog synchronization, which may
// rebuild the system prompt. Checking the actual installed tool avoids claiming
// GUI terminal capabilities for CLI, --no-tools, or a same-named extension.
func installGUITerminalPrompt(cfg *runtime.RunConfig, installed agentcore.AgentTool) {
	if installed == nil {
		return
	}
	prior := cfg.SyncTools
	cfg.SyncTools = func(ctx context.Context, ac *agentcore.AgentContext) {
		if prior != nil {
			prior(ctx, ac)
		}
		ac.SystemPrompt = strings.TrimSuffix(ac.SystemPrompt, "\n\n"+guiTerminalGuide)
		for _, tool := range ac.Tools {
			if tool == installed {
				ac.SystemPrompt += "\n\n" + guiTerminalGuide
				return
			}
		}
	}
}
