package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestComputerAutoAllowsReviewedCallsWithoutPromptAndKeepsDenials(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args string
		verdict          Level
		wantBlock        bool
	}{
		{"request", "computer_request", `{"application":"Calculator"}`, Allow, false},
		{"observe", "computer", `{"action":"snapshot"}`, Allow, false},
		{"safe control", "computer", `{"action":"click","snapshot_id":"s","ref":"@e1"}`, Allow, false},
		{"reviewed risk", "computer", `{"action":"click","snapshot_id":"s","ref":"@e1"}`, Deny, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			gate := ConsentGate(ConsentOpts{
				Mode:       permissions.New(permissions.Auto),
				Classifier: FirstParty{Next: consentClassifier{v: Verdict{Level: tc.verdict}}},
				Cwd:        t.TempDir(),
				Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
					asked = true
					return ConsentAnswer{}
				},
			})
			decision := gate(context.Background(), agentcore.AgentToolCall{ID: "computer", Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			blocked := decision != nil && decision.Block
			if asked || blocked != tc.wantBlock {
				t.Fatalf("auto asked=%t blocked=%t decision=%+v", asked, blocked, decision)
			}
		})
	}
}
