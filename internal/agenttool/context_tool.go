// This file implements the context-budget tools: get_context_remaining lets
// the model read how much room is left in the context window, and new_context
// lets it ask for a fresh window at the next turn boundary (the loop
// summarizes and keeps recent history, exactly like /compact). Both read the
// run's live budget state from the context the loop publishes, so they need no
// constructor wiring and every run — parent or sub-agent — sees its own
// figures. When no state is present (tools used outside a run loop) they
// degrade to an explanatory result rather than an error.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/contextbudget"
)

// GetContextRemainingTool reports the remaining context-window budget.
type GetContextRemainingTool struct{}

// Name implements AgentTool.
func (t *GetContextRemainingTool) Name() string { return "get_context_remaining" }

// Description implements AgentTool.
func (t *GetContextRemainingTool) Description() string {
	return "Get the remaining tokens in the current context window. " +
		"Call it before starting a long stretch of work (large refactors, many-file reads, " +
		"long test loops) so you can size the work to the room you actually have."
}

// Schema implements AgentTool. The tool takes no arguments.
func (t *GetContextRemainingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`)
}

// ExecutionMode implements AgentTool. Read-only, so parallel is safe.
func (t *GetContextRemainingTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute implements AgentTool.
func (t *GetContextRemainingTool) Execute(ctx context.Context, _ string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	state := contextbudget.FromContext(ctx)
	if state == nil {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
			"Context budget is unavailable for this run.")}}, nil
	}
	used, window, ok := state.Usage()
	if !ok {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
			"The context budget is not known yet; it is recorded as the conversation grows.")}}, nil
	}
	remaining, _ := state.Remaining()
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
		fmt.Sprintf("You have about %d tokens left in this context window (%d of %d used).",
			remaining, used, window))}}, nil
}

// NewContextTool asks the loop to start a fresh context window at the next
// turn boundary: the conversation so far is summarized and recent messages are
// kept, so work continues without replaying the whole history.
type NewContextTool struct{}

// Name implements AgentTool.
func (t *NewContextTool) Name() string { return "new_context" }

// Description implements AgentTool.
func (t *NewContextTool) Description() string {
	return "Start a fresh context window. The conversation so far is summarized " +
		"and the most recent messages are kept, then work continues in the new window. " +
		"Use it when the current task is finished and a large unrelated task is next, " +
		"or when the context is nearly full; it applies at the end of the current turn, " +
		"so finish the step you are on first. It does not change files, git state, or " +
		"any other environment state."
}

// Schema implements AgentTool. The tool takes no arguments.
func (t *NewContextTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`)
}

// ExecutionMode implements AgentTool. It mutates shared budget state, so the
// batch runs sequentially.
func (t *NewContextTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool.
func (t *NewContextTool) Execute(ctx context.Context, _ string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	state := contextbudget.FromContext(ctx)
	if state == nil {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
			"Context budget is unavailable for this run; the context window cannot be rolled over.")}}, nil
	}
	if !state.RolloverEnabled() {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
			"Starting a fresh context window is not available in this run " +
				"(automatic compaction is disabled); continue in the current window.")}}, nil
	}
	state.RequestRollover()
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(
		"A fresh context window will start after this turn: the conversation so far " +
			"will be summarized and the most recent messages kept.")}}, nil
}
