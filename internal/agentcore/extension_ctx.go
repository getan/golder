package agentcore

import "context"

type externalToolAuthorizationKey struct{}

// WithExternalToolAuthorization is set only after the executor's configured
// host gate has accepted the final call. Extension configuration is not consent.
func WithExternalToolAuthorization(ctx context.Context) context.Context {
	return context.WithValue(ctx, externalToolAuthorizationKey{}, true)
}

func ExternalToolAuthorized(ctx context.Context) bool {
	v, _ := ctx.Value(externalToolAuthorizationKey{}).(bool)
	return v
}
