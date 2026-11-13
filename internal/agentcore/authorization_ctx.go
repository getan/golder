package agentcore

import "context"

// AuthorizationDecision is the host's structured answer, not an inference from
// command output. The executing tool adds its actual envelope separately.
type AuthorizationDecision struct {
	Mode            string `json:"mode"`
	Source          string `json:"source,omitempty"`
	Reason          string `json:"reason"`
	Rationale       string `json:"rationale,omitempty"`
	EscapeRequested bool   `json:"escape_requested"`
	EscapeGranted   bool   `json:"escape_granted"`
}
type authorizationKey struct{}

func WithAuthorizationDecision(ctx context.Context, a AuthorizationDecision) context.Context {
	return context.WithValue(ctx, authorizationKey{}, a)
}
func AuthorizationFromContext(ctx context.Context) (AuthorizationDecision, bool) {
	a, ok := ctx.Value(authorizationKey{}).(AuthorizationDecision)
	return a, ok
}
