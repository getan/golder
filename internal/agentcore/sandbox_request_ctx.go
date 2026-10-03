package agentcore

import "context"

// sandboxRequestKey is the unexported context key under which a sandbox
// request from the permission layer is carried to the executing tool.
type sandboxRequestKey struct{}

// WithSandboxRequest returns a child context that asks the tool to run its
// call under OS-level isolation. The tool executor publishes it when the
// beforeToolCall hook returns a decision with Sandbox set, so the requirement
// travels the same gate → executor → tool path as every other permission
// outcome and needs no per-driver wiring.
func WithSandboxRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, sandboxRequestKey{}, true)
}

// SandboxRequestedFromContext reports whether the permission layer asked for
// this call to run isolated. A tool that can isolate (bash via sandbox-exec)
// routes itself into the sandbox; a tool that cannot must refuse rather than
// run unconfined.
func SandboxRequestedFromContext(ctx context.Context) bool {
	requested, _ := ctx.Value(sandboxRequestKey{}).(bool)
	return requested
}
