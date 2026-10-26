// This file provides the single cli-layer assembly helper that composes hook
// dispatch into a runtime.RunConfig. It lives in the cli layer (not runtime) to
// avoid a runtime→hooks→runtime import cycle: runtime stays hook-agnostic and
// only exposes the generic seams (BeforeToolCall/AfterToolCall/ShouldStopAfterTurn),
// while this helper knows about both the resolved Config.Hooks and the seams.
//
// BuildDispatcher builds a dispatcher (or short-circuits to nil when no hooks
// are configured, FR-18) and InstallSeams wires the per-event seams the issues
// #420–#424 landed: PrepareArguments/PreToolUse, AfterToolCall/PostToolUse and
// the Stop family.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/diagtrace"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/runtime"
)

// HookDeps carries the run-scoped context a Dispatcher needs: the session id and
// project directory that populate each HookInput / the hook process environment,
// and the writer that receives isolation warnings. WarnLog may be nil (defaults
// to os.Stderr), matching the plugin.EventNotifier convention.
type HookDeps struct {
	SessionID  string
	ProjectDir string
	WarnLog    io.Writer
}

// BuildDispatcher builds the run's Dispatcher from the resolved hook set without
// touching a RunConfig. It is the entry point for the multi-turn drivers (REPL /
// TUI) that resolve hooks and fire SessionStart once per session, then install
// the per-turn seams (InstallSeams) on each turn's freshly-built RunConfig. It
// returns nil for an empty set (FR-18), so callers gate all hook work on non-nil.
func BuildDispatcher(set hooks.HookSet, deps HookDeps) *hooks.Dispatcher {
	warn := deps.WarnLog
	if warn == nil {
		warn = os.Stderr
	}
	total := 0
	for _, configured := range set {
		total += len(configured)
	}
	diagtrace.Event("hooks", "dispatcher hooks=%d", total)
	return hooks.NewDispatcher(set, deps.ProjectDir, warn)
}

// InstallSeams wires the tool-execution and Stop hook points onto cfg from an
// already-built dispatcher. It is the shared install body every driver and the
// per-turn install path for multi-turn drivers use. A nil dispatcher is a no-op,
// so callers can invoke it unconditionally.
//
// PreToolUse runs at PrepareArguments: its rewrite is schema-validated and then
// the trust, boundary and authorization gates see the final input exactly ONCE.
// A hook block stops the call before any gate. PostToolUse is
// chained onto AfterToolCall as a last-writer so it can append feedback to an
// already-executed tool's result without undoing it. The Stop hook is chained
// onto the loop's natural-end seam (FR-10), bounded by the decorator's
// consecutive-block counter (FR-12).
func InstallSeams(cfg *runtime.RunConfig, d *hooks.Dispatcher, deps HookDeps) {
	if d == nil {
		return
	}
	tec := &cfg.Batch.ToolExecutorConfig
	tec.PrepareArguments = chainPrepareArguments(tec.PrepareArguments, preToolCallHook(d, deps))
	tec.AfterToolCall = chainAfterToolCall(tec.AfterToolCall, postToolCallHook(d, deps))
	installStopHook(cfg, d, deps, "Stop")
}

// preToolCallHook reads the user's PreToolUse decision before any authorization.
// It returns prepared arguments or a block error, not a gate decision.
func preToolCallHook(d *hooks.Dispatcher, deps HookDeps) agentcore.PrepareArgumentsFunc {
	return func(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error) {
		dec := d.Dispatch(ctx, hooks.EventPreToolUse, toolName, hooks.HookInput{
			EventType:  hooks.EventPreToolUse,
			SessionID:  deps.SessionID,
			ProjectDir: deps.ProjectDir,
			ToolName:   toolName,
			ToolInput:  args,
		})
		if dec.Block {
			return nil, fmt.Errorf("%s", hookReason(dec.Reason, toolName))
		}
		if len(dec.UpdatedInput) > 0 {
			return dec.UpdatedInput, nil
		}
		return args, nil
	}
}

// chainPrepareArguments applies any earlier argument preparation before the
// user hook, then carries the hook's final input into the ONE authorization
// pass. A block is an error result, not a second gate invocation.
func chainPrepareArguments(prev, hook agentcore.PrepareArgumentsFunc) agentcore.PrepareArgumentsFunc {
	return func(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error) {
		if prev != nil {
			var err error
			args, err = prev(ctx, toolName, args)
			if err != nil {
				return nil, err
			}
		}
		return hook(ctx, toolName, args)
	}
}

// postToolCallHook adapts the dispatcher's PostToolUse event to the AfterToolCall
// seam. It dispatches with the tool name, raw arguments, and the tool's response,
// then appends any reason/additionalContext to the result content as a new text
// block (the executed tool is never undone). A block on Post is treated as
// feedback only: it cannot retract an already-run tool, so we surface the reason.
func postToolCallHook(d *hooks.Dispatcher, deps HookDeps) agentcore.AfterToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		var resp json.RawMessage
		if b, err := json.Marshal(result.Content); err == nil {
			resp = b
		}
		dec := d.Dispatch(ctx, "PostToolUse", call.Name, hooks.HookInput{
			EventType:    "PostToolUse",
			SessionID:    deps.SessionID,
			ProjectDir:   deps.ProjectDir,
			ToolName:     call.Name,
			ToolInput:    call.Arguments,
			ToolResponse: resp,
		})
		feedback := joinHookText(dec.Reason, dec.AdditionalContext)
		if feedback == "" {
			return nil
		}
		content := append(agentcore.ContentList{}, result.Content...)
		content = append(content, agentcore.NewTextContent(feedback))
		return &agentcore.AfterToolCallResult{Content: &content}
	}
}

// hookReason returns the block reason, falling back to a generic message keyed on
// the tool name when the hook gave no reason.
func hookReason(reason, toolName string) string {
	if reason != "" {
		return reason
	}
	return "tool " + toolName + " blocked by PreToolUse hook"
}

// joinHookText joins two hook text fields with a newline, dropping empties.
func joinHookText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}

// chainAfterToolCall composes two AfterToolCall seams into one that runs prev
// first, then next. next's non-nil result wins (last writer), so a hook layered
// after an existing seam can override it; when next returns nil, prev's result
// is preserved. A nil operand is identity.
func chainAfterToolCall(prev, next agentcore.AfterToolCallFunc) agentcore.AfterToolCallFunc {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ctx context.Context, call agentcore.AgentToolCall, result agentcore.AgentToolResult, isError bool) *agentcore.AfterToolCallResult {
		prevRes := prev(ctx, call, result, isError)
		if nextRes := next(ctx, call, result, isError); nextRes != nil {
			return nextRes
		}
		return prevRes
	}
}
