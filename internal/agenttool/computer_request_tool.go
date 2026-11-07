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
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// ComputerRequestTool asks the local frontend to resolve an application and
// prepare its target-bound desktop session. The frontend handles ambiguity and
// owns the session lifecycle; the model cannot supply paths or window handles.
type ComputerRequestTool struct {
	Request func(context.Context, string) error
	Mode    *permissions.State
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
	return "When the user asks to use a native desktop app and no computer session is active, request it by application name. The local frontend can start an unambiguous app and select its unique window automatically; it asks the person to resolve ambiguity. End the turn and wait for the frontend to continue when the session is ready. Do not ask the person to launch the app or type a command. Use browser for websites."
}

func (*ComputerRequestTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["application"],"properties":{"application":{"type":"string","minLength":1,"maxLength":128,"description":"Application name requested by the user, not a path or window handle."}}}`)
}

func (*ComputerRequestTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// CheckComputerLaunch applies the current session mode and enforced OS
// containment both when a request is queued and immediately before launch.
func CheckComputerLaunch(mode *permissions.State) error {
	if mode != nil && mode.Mode() == permissions.ReadOnly {
		return errors.New("read-only mode does not permit launching or controlling applications")
	}
	if seatbelt.ModeFromEnv() == seatbelt.ModeEnforce {
		return errors.New("desktop application launch cannot satisfy required OS containment")
	}
	return nil
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
	if err := CheckComputerLaunch(t.Mode); err != nil {
		return errorResult("computer request: " + err.Error()), nil
	}
	if agentcore.SandboxRequestedFromContext(ctx) {
		return errorResult("computer request: desktop application launch cannot satisfy required OS containment"), nil
	}
	if err := ctx.Err(); err != nil {
		return agentcore.AgentToolResult{}, err
	}
	if !t.pending.CompareAndSwap(false, true) {
		return errorResult("computer request: application setup is already pending"), nil
	}
	if err := t.Request(ctx, strings.TrimSpace(args.Application)); err != nil {
		t.pending.Store(false)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return agentcore.AgentToolResult{}, err
		}
		return errorResult("computer request: " + err.Error()), nil
	}
	// Frontends resolve the application only after the current run settles. A
	// textual instruction alone leaves that handoff dependent on the model.
	stop := true
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("Application setup requested. End this turn; the frontend will continue when the window is ready, or ask the person to resolve an ambiguous choice.")}, Terminate: &stop}, nil
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
