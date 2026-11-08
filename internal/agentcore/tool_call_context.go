package agentcore

import "context"

type toolCallIDKey struct{}

// WithToolCallID carries the current call's identity for presentation only.
// It is not an authorization grant and an empty id clears an inherited id.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the exact current tool call's id, if known.
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
