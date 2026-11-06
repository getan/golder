package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestLarkPolicyApprovalFollowsPermissionMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        permissions.Mode
		verdict     Verdict
		answer      bool
		block       bool
		human       bool
		asked       int
		unavailable bool
	}{
		{name: "auto allow", mode: permissions.Auto, verdict: Verdict{Level: Allow, Network: true}},
		{name: "auto confirm", mode: permissions.Auto, verdict: Verdict{Level: Confirm, Network: true}},
		{name: "auto deny", mode: permissions.Auto, verdict: Verdict{Level: Deny, Network: true}, block: true},
		{name: "auto contain", mode: permissions.Auto, verdict: Verdict{Level: Contain, Network: true}, block: true},
		{name: "auto review unavailable", mode: permissions.Auto, verdict: Verdict{Level: Confirm, Network: true, Failed: true}, block: true, unavailable: true},
		{name: "ask approved", mode: permissions.Ask, verdict: Verdict{Level: Allow, Network: true}, answer: true, human: true, asked: 1},
		{name: "ask refused", mode: permissions.Ask, verdict: Verdict{Level: Allow, Network: true}, block: true, asked: 1},
		{name: "read only", mode: permissions.ReadOnly, verdict: Verdict{Level: Allow, Network: true}, block: true},
		{name: "full access", mode: permissions.FullAccess, verdict: Verdict{Level: Deny}, human: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := 0
			var notes []Note
			gate := ConsentGate(ConsentOpts{
				Mode: permissions.New(tc.mode), Cwd: t.TempDir(), Classifier: consentClassifier{v: tc.verdict},
				Notify: func(n Note) { notes = append(notes, n) },
				Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
					asked++
					return ConsentAnswer{Answered: true, Allow: tc.answer, Needs: req.Needs}
				},
			})
			call := agentcore.AgentToolCall{ID: "write", Name: "mcp__lark__doc_write", Arguments: json.RawMessage(`{"operation":"append","options":{"doc":"example-doc","content":"test"},"confirm":true}`), RequirePolicyApproval: true}
			dec := gate(context.Background(), call)
			if dec == nil || dec.Block != tc.block || dec.PolicyApproved == tc.block || dec.OperatorConfirmed != tc.human || asked != tc.asked {
				t.Fatalf("decision=%+v asked=%d want blocked=%t human=%t asked=%d", dec, asked, tc.block, tc.human, tc.asked)
			}
			if tc.unavailable && (len(notes) == 0 || notes[len(notes)-1].Kind != NoteUnavailable) {
				t.Fatalf("review failure was not distinguished from a denial: %+v", notes)
			}
		})
	}
}

func TestPolicyApprovalDoesNotOverrideHumanOnlyRequirement(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.Auto, permissions.FullAccess} {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(mode), Cwd: t.TempDir(), Classifier: consentClassifier{v: Verdict{Level: Allow}}})
		call := agentcore.AgentToolCall{ID: "claim", Name: "tuios", Arguments: json.RawMessage(`{"action":"claim","window":"w2","scope":"session","host_control":true}`), RequireOperatorConfirmation: true, RequirePolicyApproval: true}
		if dec := gate(context.Background(), call); dec == nil || !dec.Block || dec.PolicyApproved || dec.OperatorConfirmed {
			t.Fatalf("%s changed a human-only control into an automatic grant: %+v", mode, dec)
		}
	}
}
