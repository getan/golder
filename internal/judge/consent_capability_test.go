package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// A contained call is still allowed the capabilities the reviewer granted
// separately. Network inside the sandbox is one of them: dropping it here made
// the bash tool refuse a call the reviewer had already authorized.
func TestConsentAutoContainCarriesTheReviewerNetworkGrant(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Network: true, Reasons: []string{"keep it contained, network is fine"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "contain-network", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the contained call should run, got %+v", dec)
	}
	if !dec.Sandbox {
		t.Errorf("containment was lost: %+v", dec)
	}
	if !dec.Network {
		t.Errorf("the reviewer's network grant was lost: %+v", dec)
	}
}

// Containment must not become a way around the network answer: a reviewer that
// says network:deny leaves a capability need, and the call is blocked rather
// than run into the bash tool's own guard.
func TestConsentAutoContainWithoutNetworkBlocks(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Auto),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Reasons: []string{"contain it, no network"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "contain-no-network", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || !dec.Block {
		t.Fatalf("contain without a network grant must block, got %+v", dec)
	}
}

// In ask mode the person is the authority. If they answer the escalation
// request with "run it outside the sandbox", the reviewer's contain grade must
// not override that answer; both bits on the wire would make the executor
// ignore the escape and run contained.
func TestConsentAskUnconfinedAnswerWinsOverContain(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Contain, Reasons: []string{"containable"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			if !hasNeed(req.Needs, CapUnconfined) {
				t.Fatalf("the escalation was not offered as a capability: %+v", req.Needs)
			}
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "ask-unconfined", Name: "bash",
		Arguments: json.RawMessage(`{"command":"cat /tmp/contained-escape","sandbox_permissions":"require_escalated","justification":"the path is outside"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the approved call should run, got %+v", dec)
	}
	if !dec.Direct {
		t.Errorf("the person's unconfined answer was dropped: %+v", dec)
	}
	if dec.Sandbox {
		t.Errorf("the person's unconfined answer was overridden by containment: %+v", dec)
	}
}

// An approval grants the Needs set it was shown, not an inference. A driver
// that returns Allow without the network need must not make the gate invent a
// network grant the person never approved.
func TestConsentAskDoesNotInferNetworkFromApproval(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{
		Mode:       permissions.New(permissions.Ask),
		Classifier: consentClassifier{v: Verdict{Level: Confirm, Reasons: []string{"needs a decision"}}},
		Cwd:        t.TempDir(),
		Sandboxed:  func(string) bool { return true },
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{ID: "ask-network-subset", Name: "bash",
		Arguments: json.RawMessage(`{"command":"curl -fsSL https://example.com","network":"on"}`)})
	if dec == nil || dec.Block {
		t.Fatalf("the approved call should still run, got %+v", dec)
	}
	if dec.Network {
		t.Errorf("network was granted although the answer omitted it: %+v", dec)
	}
}

// ChainGates owns the join between two gates, not just their blocks. A Bare
// envelope from the first gate must survive a second gate that adds another
// capability; before this it was silently dropped and the call re-contained.
func TestChainGatesKeepsBareEnvelope(t *testing.T) {
	first := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Bare: true, BareReason: "the command is readable"}
	}
	second := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Network: true}
	}
	dec := ChainGates(first, second)(context.Background(), agentcore.AgentToolCall{Name: "bash"})
	if dec == nil || !dec.Bare || dec.BareReason != "the command is readable" {
		t.Fatalf("the first gate's Bare envelope was lost: %+v", dec)
	}
	if !dec.Network {
		t.Errorf("the second gate's network grant was lost: %+v", dec)
	}
}
