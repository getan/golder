package agentcore

import "context"

// AgentOrigin names the run a question came from, when it is not the session's
// own conversation: a sub-agent's dispatch. It travels on the context so every
// gate that asks a person something can say WHO is asking — a dialog that shows
// only "read /outside/x" leaves the person unable to tell a call the model made
// in front of them from one a delegated child made on its own.
//
// It is a display label, never a permission: nothing in the gate chain branches
// on it, because "which agent asked" does not change what the user is being
// asked to consent to. A nested dispatch appends, so "task › explore" reads as
// the chain that produced the question.
type agentOriginKey struct{}

// originSeparator joins a parent origin to a child's, so a nested sub-agent's
// label shows the path rather than only the innermost name.
const originSeparator = " › "

// WithAgentOrigin returns a child context stamped with name as the asking
// agent. An empty name leaves ctx unchanged (a direct run needs no label), and
// a nested call appends to the origin already carried.
func WithAgentOrigin(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	if prior := OriginFromContext(ctx); prior != "" {
		name = prior + originSeparator + name
	}
	return context.WithValue(ctx, agentOriginKey{}, name)
}

// OriginFromContext returns the asking agent's label, or "" when the run is the
// session's own (or the ctx never carried one).
func OriginFromContext(ctx context.Context) string {
	name, _ := ctx.Value(agentOriginKey{}).(string)
	return name
}
