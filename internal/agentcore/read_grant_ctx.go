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

// writeGrantKey is the unexported context key carrying one call's write grant
// from the boundary gate to the executing tool.
type writeGrantKey struct{}

// WithWriteGrant returns a child context that allows the tool to write one path
// outside the workspace. It is the write side's twin of WithReadGrant: the
// executor publishes it when the gate returns a decision with WriteGrant set —
// the "allow this write once" answer to the boundary question about a patch —
// so the grant lives exactly as long as this call.
func WithWriteGrant(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, writeGrantKey{}, path)
}

// WriteGrantFromContext returns the one-call write grant, or "" when this call
// has none. Only a tool that writes honours it, and only for the path named:
// the answer covers the one file the user was asked about, not the tree holding
// it — that answer is the session grant, and it lives in the authorization
// table where every reader can see it.
func WriteGrantFromContext(ctx context.Context) string {
	path, _ := ctx.Value(writeGrantKey{}).(string)
	return path
}
