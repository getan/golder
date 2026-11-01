package agentcore

import "context"

// executionBindingKey carries a gate-computed, non-secret fingerprint to the
// executing tool. It binds an outside-sandbox authorization to the local facts
// reviewed for that exact call; it is not itself an authorization.
type executionBindingKey struct{}

func WithExecutionBinding(ctx context.Context, binding string) context.Context {
	if binding == "" {
		return ctx
	}
	return context.WithValue(ctx, executionBindingKey{}, binding)
}

func ExecutionBindingFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	binding, _ := ctx.Value(executionBindingKey{}).(string)
	return binding
}
