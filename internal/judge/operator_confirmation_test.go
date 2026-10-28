package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestOperatorConfirmationIsNotAnAutomaticReviewerDecision(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.Auto, permissions.FullAccess, permissions.ReadOnly, permissions.Ask} {
		t.Run(mode.String(), func(t *testing.T) {
			asked := 0
			gate := ConsentGate(ConsentOpts{Mode: permissions.New(mode), Cwd: t.TempDir(),
				Classifier: consentClassifier{v: Verdict{Level: Allow}},
				Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
					asked++
					return ConsentAnswer{Answered: true, Allow: true}
				},
			})
			call := agentcore.AgentToolCall{ID: "write-1", Name: "mcp__lark__message_send", Arguments: json.RawMessage(`{"confirm":true}`), RequireOperatorConfirmation: true}
			dec := gate(context.Background(), call)
			if mode == permissions.Ask {
				if dec == nil || dec.Block || !dec.OperatorConfirmed || asked != 1 {
					t.Fatalf("exact confirmation was not obtained: %+v asked=%d", dec, asked)
				}
			} else if dec == nil || !dec.Block || dec.OperatorConfirmed || asked != 0 {
				t.Fatalf("automatic mode invented human approval: %+v asked=%d", dec, asked)
			}
		})
	}
}
