package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestAttestedReuseDoesNotRequestAnotherRuntimeLaunch(t *testing.T) {
	call := agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"open","browser":"owned"}`)}
	for _, attested := range []bool{false, true} {
		for _, launch := range []bool{false, true} {
			ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, call.Arguments, agentcore.ToolReviewEvidence{RuntimeAttested: attested, LaunchRequired: launch})
			needs := collectNeeds(ctx, ConsentOpts{BrowserRuntimeOutside: true}, call, Verdict{Level: Allow}, permissions.Ask, "en")
			hasRuntime := false
			for _, need := range needs {
				hasRuntime = hasRuntime || need.Cap == CapBrowserRuntime
			}
			if hasRuntime != (!attested || launch) {
				t.Fatalf("attested=%t launch=%t: %+v", attested, launch, needs)
			}
		}
	}
	// Reusing the facts for another call cannot suppress its launch requirement.
	ctx := agentcore.WithToolReviewEvidence(context.Background(), call.Name, json.RawMessage(`{"action":"status"}`), agentcore.ToolReviewEvidence{RuntimeAttested: true})
	needs := collectNeeds(ctx, ConsentOpts{BrowserRuntimeOutside: true}, call, Verdict{Level: Allow}, permissions.Ask, "en")
	if len(needs) == 0 || needs[0].Cap != CapBrowserRuntime {
		t.Fatal("a different call reused runtime facts")
	}
}
