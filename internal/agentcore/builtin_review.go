package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

// builtinReviewKey carries host-attested tool identity, never model arguments
// or a tool-provided claim. Every dispatch replaces the inherited value.
type builtinReviewKey struct{}

type builtinReviewBinding struct {
	name   string
	callID string
	digest [32]byte
}

// WithBuiltinReview binds the executor's exact built-in implementation to the
// current call and validated arguments. An empty name clears inherited proof.
func WithBuiltinReview(ctx context.Context, name string, args json.RawMessage) context.Context {
	return context.WithValue(ctx, builtinReviewKey{}, builtinReviewBinding{
		name: name, callID: ToolCallIDFromContext(ctx), digest: sha256.Sum256(args),
	})
}

// BuiltinReviewAttested reports only proof for the exact current call. It is
// not itself a capability grant; the normal authorization gate still runs.
func BuiltinReviewAttested(ctx context.Context, name string, args json.RawMessage) bool {
	if ctx == nil || name == "" {
		return false
	}
	binding, ok := ctx.Value(builtinReviewKey{}).(builtinReviewBinding)
	return ok && binding.name == name && binding.callID == ToolCallIDFromContext(ctx) && binding.digest == sha256.Sum256(args)
}
