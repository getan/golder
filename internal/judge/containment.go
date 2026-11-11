package judge

import (
	"context"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

func canContainCall(ctx context.Context, opts ConsentOpts, call agentcore.AgentToolCall) bool {
	if strings.EqualFold(strings.TrimSpace(call.Name), "browser") {
		// A target already running outside the outer sandbox cannot be moved
		// into it by setting Sandbox on this call. Unknown runtime is not proof.
		evidence, ok := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
		if !ok || !evidence.CanSandbox {
			return false
		}
	}
	return opts.Sandboxed != nil && opts.Sandboxed(call.Name)
}
