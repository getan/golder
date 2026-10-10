package agentcore

import "context"

// readGrantKey is the unexported context key carrying one call's read grant
// from the permission layer to the executing tool.
type readGrantKey struct{}

// WithReadGrant returns a child context that allows the tool to read one path
// outside the workspace. The tool executor publishes it when the beforeToolCall
// hook returns a decision with ReadGrant set — the "allow once" answer to the
// workspace-boundary question — so the grant lives exactly as long as this call
// and needs no per-driver wiring.
func WithReadGrant(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, readGrantKey{}, path)
}

// ReadGrantFromContext returns the one-call read grant, or "" when this call
// has none. A tool that reads files (read, view_image, grep, find, ls) honours
// it once; a tool that writes must not, since the grant answers a question
// about consent to read.
func ReadGrantFromContext(ctx context.Context) string {
	path, _ := ctx.Value(readGrantKey{}).(string)
	return path
}
