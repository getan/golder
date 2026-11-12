package agentcore

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBuiltinReviewProofIsBoundAndCanBeCleared(t *testing.T) {
	args := json.RawMessage(`{"todos":[]}`)
	ctx := WithToolCallID(context.Background(), "call-1")
	ctx = WithBuiltinReview(ctx, "todo", args)
	if !BuiltinReviewAttested(ctx, "todo", args) {
		t.Fatal("matching built-in call lost its proof")
	}
	if BuiltinReviewAttested(ctx, "other", args) || BuiltinReviewAttested(ctx, "todo", json.RawMessage(`{"todos":[{}]}`)) {
		t.Fatal("proof was accepted for a different tool or arguments")
	}
	if BuiltinReviewAttested(WithToolCallID(ctx, "call-2"), "todo", args) {
		t.Fatal("proof was inherited by another call")
	}
	if BuiltinReviewAttested(WithBuiltinReview(ctx, "", args), "todo", args) {
		t.Fatal("cleared proof still authorized the previous call")
	}
}
