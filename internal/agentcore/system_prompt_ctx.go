package agentcore

import "context"

// systemPromptSnapshotKey carries the parent run's system prompt for a tool
// call that derives another run (the generic task tool). The conversation
// snapshot alone is not enough for a fork: the child also needs the project
// instructions, environment and skill guidance the parent is running under.
type systemPromptSnapshotKey struct{}

// WithSystemPromptSnapshot returns a child context carrying the system prompt
// the parent run uses for this tool batch.
func WithSystemPromptSnapshot(ctx context.Context, prompt string) context.Context {
	return context.WithValue(ctx, systemPromptSnapshotKey{}, prompt)
}

// SystemPromptSnapshotFromContext returns the parent system prompt carried by
// ctx, or "" when the caller runs outside a run loop (a direct tool call, an
// embedder, or a test). Callers fall back to their own system prompt.
func SystemPromptSnapshotFromContext(ctx context.Context) string {
	prompt, _ := ctx.Value(systemPromptSnapshotKey{}).(string)
	return prompt
}
