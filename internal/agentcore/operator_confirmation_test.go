package agentcore

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOperatorConfirmationBindsExactCall(t *testing.T) {
	args := json.RawMessage(`{"to":"chat-a","text":"hello"}`)
	ctx := WithOperatorConfirmation(context.Background(), "one", "send", args)
	if !OperatorConfirmedFor(ctx, "one", "send", args) || OperatorConfirmedFor(ctx, "two", "send", args) || OperatorConfirmedFor(ctx, "one", "delete", args) || OperatorConfirmedFor(ctx, "one", "send", json.RawMessage(`{"to":"chat-b","text":"hello"}`)) {
		t.Fatal("confirmation was not bound to call ID, tool and exact prepared arguments")
	}
}

func TestPolicyApprovalBindsExactCallWithoutImplyingHumanConfirmation(t *testing.T) {
	args := json.RawMessage(`{"to":"chat-a","text":"hello"}`)
	ctx := WithPolicyApproval(context.Background(), "one", "send", args)
	if !PolicyApprovedFor(ctx, "one", "send", args) || PolicyApprovedFor(ctx, "two", "send", args) || PolicyApprovedFor(ctx, "one", "delete", args) || PolicyApprovedFor(ctx, "one", "send", json.RawMessage(`{"to":"chat-b","text":"hello"}`)) {
		t.Fatal("policy approval was not bound to the exact prepared call")
	}
	if OperatorConfirmedFor(ctx, "one", "send", args) {
		t.Fatal("automatic policy approval was treated as human confirmation")
	}
	human := WithOperatorConfirmation(context.Background(), "one", "send", args)
	if PolicyApprovedFor(human, "one", "send", args) {
		t.Fatal("human-only confirmation implicitly granted a policy decision")
	}
}
