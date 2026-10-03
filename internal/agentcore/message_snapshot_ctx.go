package agentcore

import "context"

// messageSnapshotKey is the unexported context key under which a read-only
// snapshot of the conversation messages is stored for the tool batch.
type messageSnapshotKey struct{}

// WithMessageSnapshot returns a child context carrying a snapshot of the
// conversation as of the tool batch dispatch. Context-sensitive collaborators
// (the risk judge) read it to grade a call against the user intent and recent
// history. The slice header is a copy: the loop appends only past its length,
// so the captured view stays stable.
func WithMessageSnapshot(ctx context.Context, msgs MessageList) context.Context {
	return context.WithValue(ctx, messageSnapshotKey{}, msgs)
}

// MessageSnapshotFromContext returns the conversation snapshot carried by ctx,
// or nil when the caller runs outside a run loop (e.g. a remote-control
// confirm seam); callers must degrade to context-free grading rather than
// failing.
func MessageSnapshotFromContext(ctx context.Context) MessageList {
	msgs, _ := ctx.Value(messageSnapshotKey{}).(MessageList)
	return msgs
}
