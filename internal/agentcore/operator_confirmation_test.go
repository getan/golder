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
