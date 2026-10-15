package agentcore

import "context"

type networkGrantKey struct{}
type directGrantKey struct{}

// WithNetworkGrant records the authorization gate's decision, not the tool's
// request. The execution layer cannot infer this capability from arguments.
func WithNetworkGrant(ctx context.Context) context.Context {
	return context.WithValue(ctx, networkGrantKey{}, true)
}

func NetworkGrantedFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(networkGrantKey{}).(bool)
	return v
}

// WithDirectGrant marks an explicitly authorized one-call sandbox escape.
func WithDirectGrant(ctx context.Context) context.Context {
	return context.WithValue(ctx, directGrantKey{}, true)
}

func DirectGrantedFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(directGrantKey{}).(bool)
	return v
}
