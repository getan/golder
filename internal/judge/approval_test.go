package judge

// Tests for the Confirm callback: the three cases that used to fail closed in
// a non-interactive driver now ask the user, and an unanswered request still
// fails closed (silence is never approval).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// answeringConfirm builds a Confirm callback that records the request and
// returns a fixed answer.
func answeringConfirm(seen *ApprovalRequest, ans ApprovalAnswer) func(context.Context, ApprovalRequest) ApprovalAnswer {
	return func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
		if seen != nil {
			*seen = req
		}
		return ans
	}
}

func bashCall(cmd string) agentcore.AgentToolCall {
	args, _ := json.Marshal(map[string]string{"command": cmd})
	return agentcore.AgentToolCall{ID: "call-1", Name: "bash", Arguments: args}
}

// TestConfirmCallbackApprovesConfirmTier: ask mode + Confirm verdict + a
// callback that approves runs the call.
func TestConfirmCallbackApprovesConfirmTier(t *testing.T) {
	state := permissions.New(permissions.Ask)
	var seen ApprovalRequest
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm, Risk: "medium", Authorization: "high", Reasons: []string{"installs deps"}}},
		Confirm:    answeringConfirm(&seen, ApprovalAnswer{Approve: true, Answered: true}),
	})
	if dec := gate(context.Background(), bashCall("npm install")); dec != nil {
		t.Fatalf("approved call should pass, got %+v", dec)
	}
	if seen.Kind != ApprovalConfirm || seen.Tool != "bash" || !strings.Contains(seen.Summary, "npm install") {
		t.Errorf("callback saw %+v, want a confirm request for npm install", seen)
	}
	if seen.Rationale != "installs deps" || seen.Risk == "" {
		t.Errorf("callback should carry the verdict context, got %+v", seen)
	}
}

// TestConfirmCallbackDeniesConfirmTier: a denial blocks with guidance.
func TestConfirmCallbackDeniesConfirmTier(t *testing.T) {
	state := permissions.New(permissions.Ask)
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm}},
		Confirm:    answeringConfirm(nil, ApprovalAnswer{Answered: true}),
	})
	dec := gate(context.Background(), bashCall("npm install"))
	if dec == nil || !dec.Block {
		t.Fatalf("denied call should block, got %+v", dec)
	}
	if dec.Content == nil || !strings.Contains(agentcore.ContentToText(*dec.Content), "denied by the user") {
		t.Errorf("denial should tell the model what happened, got %+v", dec)
	}
}

// TestConfirmCallbackUnansweredFailsClosed: an unanswered request (cancelled
// run, no UI) blocks rather than allowing by silence.
func TestConfirmCallbackUnansweredFailsClosed(t *testing.T) {
	state := permissions.New(permissions.Ask)
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm}},
		Confirm:    answeringConfirm(nil, ApprovalAnswer{}), // Answered=false
	})
	dec := gate(context.Background(), bashCall("npm install"))
	if dec == nil || !dec.Block {
		t.Fatalf("unanswered request must fail closed, got %+v", dec)
	}
}

// TestConfirmCallbackNoSandboxTier: a Sandbox verdict the execution layer
// cannot isolate asks the user instead of failing closed.
func TestConfirmCallbackNoSandboxTier(t *testing.T) {
	state := permissions.New(permissions.Ask)
	var seen ApprovalRequest
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Sandbox}},
		Sandboxed:  func(string) bool { return false }, // no runner for this call
		Confirm:    answeringConfirm(&seen, ApprovalAnswer{Approve: true, Answered: true}),
	})
	if dec := gate(context.Background(), bashCall("rm -rf build")); dec != nil {
		t.Fatalf("approved call should pass, got %+v", dec)
	}
	if seen.Kind != ApprovalNoSandbox {
		t.Errorf("kind = %q, want %q", seen.Kind, ApprovalNoSandbox)
	}
}

// TestConfirmCallbackReviewFailedTier: a failed review with no containment
// asks the user, carrying the failure rationale.
func TestConfirmCallbackReviewFailedTier(t *testing.T) {
	state := permissions.New(permissions.Ask)
	var seen ApprovalRequest
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm, Failed: true, Reasons: []string{"reviewer timed out"}}},
		Sandboxed:  func(string) bool { return false },
		Confirm:    answeringConfirm(&seen, ApprovalAnswer{Approve: true, Answered: true}),
	})
	if dec := gate(context.Background(), bashCall("echo hi")); dec != nil {
		t.Fatalf("approved call should pass, got %+v", dec)
	}
	if seen.Kind != ApprovalReviewFailed {
		t.Errorf("kind = %q, want %q", seen.Kind, ApprovalReviewFailed)
	}
	if seen.Rationale != "reviewer timed out" {
		t.Errorf("rationale = %q, want the review failure", seen.Rationale)
	}
}

// TestConfirmCallbackNotUsedInAutoMode keeps the auto-mode behavior: the
// reviewer decides, no dialog.
func TestConfirmCallbackNotUsedInAutoMode(t *testing.T) {
	state := permissions.New(permissions.Auto)
	asked := false
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm}},
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked = true
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), bashCall("npm install")); dec != nil {
		t.Fatalf("auto mode should approve Confirm-tier calls, got %+v", dec)
	}
	if asked {
		t.Error("auto mode must not open the approval dialog")
	}
}

// TestConfirmCallbackEscalationJustification: a bash escalation request's
// justification reaches the dialog.
func TestConfirmCallbackEscalationJustification(t *testing.T) {
	state := permissions.New(permissions.Ask)
	var seen ApprovalRequest
	gate := PermissionGate(state, GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Confirm}},
		Confirm:    answeringConfirm(&seen, ApprovalAnswer{Answered: true}),
	})
	args, _ := json.Marshal(map[string]string{
		"command":             "go build ./...",
		"sandbox_permissions": "require_escalated",
		"justification":       "needs GOCACHE outside the workspace",
	})
	_ = gate(context.Background(), agentcore.AgentToolCall{ID: "c", Name: "bash", Arguments: args})
	if !strings.Contains(seen.Justification, "GOCACHE") {
		t.Errorf("justification = %q, want the model's reason", seen.Justification)
	}
}
