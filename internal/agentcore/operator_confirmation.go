package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

type operatorConfirmationKey struct{}
type policyApprovalKey struct{}

func confirmationDigest(id, name string, args json.RawMessage) [32]byte {
	b, _ := json.Marshal([]string{id, name, string(args)})
	return sha256.Sum256(b)
}

func WithOperatorConfirmation(ctx context.Context, id, name string, args json.RawMessage) context.Context {
	return context.WithValue(ctx, operatorConfirmationKey{}, confirmationDigest(id, name, args))
}

func OperatorConfirmedFor(ctx context.Context, id, name string, args json.RawMessage) bool {
	v, ok := ctx.Value(operatorConfirmationKey{}).([32]byte)
	return ok && v == confirmationDigest(id, name, args)
}

// WithPolicyApproval binds a host policy decision to this exact call. It does
// not assert that a person approved the call or grant any other capability.
func WithPolicyApproval(ctx context.Context, id, name string, args json.RawMessage) context.Context {
	return context.WithValue(ctx, policyApprovalKey{}, confirmationDigest(id, name, args))
}

func PolicyApprovedFor(ctx context.Context, id, name string, args json.RawMessage) bool {
	v, ok := ctx.Value(policyApprovalKey{}).([32]byte)
	return ok && v == confirmationDigest(id, name, args)
}
