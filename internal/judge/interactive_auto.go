package judge

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// autoInteractiveVerdict removes per-action model review only for the host's
// concrete interactive tools in auto. It is not a proof of a remote effect or
// permission to bypass the tools' target, pause or operator-control checks.
func autoInteractiveVerdict(ctx context.Context, opts ConsentOpts, mode permissions.Mode, call agentcore.AgentToolCall) (Verdict, bool) {
	if mode != permissions.Auto || opts.Enforce || call.RequireOperatorConfirmation || call.RequirePolicyApproval {
		return Verdict{}, false
	}
	name := strings.ToLower(strings.TrimSpace(call.Name))
	if (name != "browser" && name != "computer") || !agentcore.BuiltinReviewAttested(ctx, name, call.Arguments) {
		return Verdict{}, false
	}
	var args struct {
		Action      string `json:"action"`
		HostControl bool   `json:"host_control"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil || args.HostControl {
		return Verdict{}, false
	}
	if name == "browser" {
		switch args.Action {
		case "open", "list", "attach", "snapshot", "find", "get", "is", "wait", "dialog", "console", "errors", "network", "screenshot", "navigate", "click", "dblclick", "hover", "focus", "check", "uncheck", "fill", "type", "select", "press", "insert", "eval", "upload", "scroll", "scroll_into_view", "tab_new", "tab_close", "tab_select", "dialog_accept", "dialog_dismiss", "status", "pause", "quit":
		default:
			return Verdict{}, false
		}
		ev, ok := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
		if !ok || (args.Action != "open" && !ev.RuntimeAttested) {
			return Verdict{}, false
		}
	} else {
		switch args.Action {
		case "status", "snapshot", "screenshot", "click", "click_at", "fill", "type_text", "press", "scroll", "close_window", "pause", "stop":
		default:
			return Verdict{}, false
		}
	}
	return Verdict{Level: Confirm, Source: "auto-interactive", Risk: "unknown", Authorization: "unknown", Network: NetworkRequired(ctx, call.Name, call.Arguments), Reasons: []string{"auto mode permits the host-bound interactive action; target and session checks still apply"}}, true
}
