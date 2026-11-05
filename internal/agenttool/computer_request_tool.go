package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
)

// ComputerRequestTool can only ask the local frontend to show a target picker.
// It cannot enumerate windows, grant observation, select a target or start a
// desktop session. The frontend owns the operator confirmation and lifecycle.
type ComputerRequestTool struct {
	Request func(context.Context, string) error
	pending atomic.Bool
}

// Reset clears the pending request after the frontend resolves or cancels it.
func (t *ComputerRequestTool) Reset() {
	if t != nil {
		t.pending.Store(false)
	}
}

func (*ComputerRequestTool) Name() string { return "computer_request" }

func (*ComputerRequestTool) Description() string {
	return "When the user asks to use a native desktop app and no computer session is active, request the local operator's window selection and authorization by application name. Do not ask them to type a command. This grants no access; wait for their decision. Use browser for websites."
}

func (*ComputerRequestTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["application"],"properties":{"application":{"type":"string","minLength":1,"maxLength":128,"description":"Application name requested by the user, not a path or window handle."}}}`)
}

func (*ComputerRequestTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *ComputerRequestTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if t == nil || t.Request == nil {
		return errorResult("computer request: local operator interface unavailable"), nil
	}
	var args struct {
		Application string `json:"application"`
	}
	if json.Unmarshal(raw, &args) != nil || !validComputerApplication(args.Application) {
		return errorResult("computer request: provide a valid application name, not a path or system handle"), nil
	}
	if err := ctx.Err(); err != nil {
		return agentcore.AgentToolResult{}, err
	}
	if !t.pending.CompareAndSwap(false, true) {
		return errorResult("computer request: an operator decision is already pending"), nil
	}
	if err := t.Request(ctx, strings.TrimSpace(args.Application)); err != nil {
		t.pending.Store(false)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return agentcore.AgentToolResult{}, err
		}
		return errorResult("computer request: " + err.Error()), nil
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("Request submitted to the local operator. No window has been selected and no access has been granted. End this turn and wait for the operator's decision.")}}, nil
}

func validComputerApplication(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 512 || utf8.RuneCountInString(name) > 128 || !utf8.ValidString(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
