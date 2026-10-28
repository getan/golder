package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
)

type operatorConfirmationKey struct{}

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
