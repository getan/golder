package tui

// The TUI's trust gate: side-effect tools in a directory the user has not
// trusted need an explicit yes. The REPL asks on stdin (trust.BeforeToolCall);
// the TUI has no stdin, so before this gate existed an untrusted directory
// silently ran side-effect tools under the permission gate alone. Now the
// question goes to the same approval dialog the permission gate uses — or to
// the paired browser while remote control is connected (the pre-existing
// flow, kept as the first choice so a remote user is asked where they are).

import (
	"context"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/trust"
)

// trustApprovalGate builds the trust gate for this session. rs may be nil (no
// remote control); mgr nil disables the gate (trust unavailable, the same as
// the REPL's nil-manager no-op).
func (s *runSession) trustApprovalGate(rs *remoteSession) agentcore.BeforeToolCallFunc {
	mgr := s.trust
	if mgr == nil {
		return nil
	}
	cwd := s.hookDeps.ProjectDir
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if !trust.SideEffectTools[call.Name] {
			return nil
		}
		// Re-check under the call: an earlier "approve for this session" (or a
		// /trust while the run was in flight) makes later calls free.
		if mgr.IsTrusted(cwd) {
			return nil
		}
		summary := trust.ToolCallSummary(call)
		if rs != nil && rs.hasClient() {
			d, remote := rs.bridge.Confirm(ctx, call.Name, summary)
			if !remote || !d.Approve {
				return blockUntrustedCall(call, cwd)
			}
			if d.Always {
				mgr.SetSessionTrust(cwd)
			}
			return nil
		}
		ans := s.confirmApproval(ctx, judge.ApprovalRequest{
			Kind:          judge.ApprovalTrust,
			Tool:          call.Name,
			Summary:       summary,
			Rationale:     "this directory is not trusted",
			Authorization: "untrusted directory",
		})
		if !ans.Answered || !ans.Approve {
			return blockUntrustedCall(call, cwd)
		}
		if ans.Always {
			mgr.SetSessionTrust(cwd)
		}
		return nil
	}
}

// blockUntrustedCall refuses a side-effect call in an untrusted directory with
// guidance the model can act on (it may ask the user to trust the directory,
// or propose a different approach).
func blockUntrustedCall(call agentcore.AgentToolCall, cwd string) *agentcore.BeforeToolCallDecision {
	msg := "tool " + call.Name + " blocked: " + cwd + " is not trusted (the user can run /trust to allow it, or approve this call)"
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}
