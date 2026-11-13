package judge

import (
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func decisionCode(mode permissions.Mode, enforce bool, call agentcore.AgentToolCall, v Verdict, blocked bool) string {
	if enforce && askedToEscape(call) {
		return "sandbox_enforced"
	}
	if mode == permissions.ReadOnly && blocked {
		return "read_only"
	}
	if v.Failed {
		return "reviewer_unavailable"
	}
	if v.Level == Contain {
		return "reviewer_contained"
	}
	if blocked {
		return "reviewer_denied"
	}
	if mode == permissions.FullAccess {
		return "full_access"
	}
	return "approved"
}
