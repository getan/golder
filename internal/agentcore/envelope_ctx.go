package agentcore

// The envelope a call runs under, as the tool sees it.
//
// The gate decides (judge.EnvelopeBare), the executor publishes, the tool
// obeys — the same division as the other per-call capabilities. The zero value
// is containment: a tool that finds nothing in its context isolates the call.
// That is the safe default and the reason this is a positive fact rather than a
// flag "not to sandbox".

import "context"

type bareEnvelopeKey struct{}

// WithBareEnvelope marks ctx as one whose call may run outside the OS sandbox,
// carrying the reason so the transcript can say why.
func WithBareEnvelope(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, bareEnvelopeKey{}, reason)
}

// BareEnvelopeFromContext reports the gate's envelope decision: ok is true when
// the call may run unconfined, with the reason it may. ok=false is the default
// and means "contain it".
func BareEnvelopeFromContext(ctx context.Context) (reason string, ok bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(bareEnvelopeKey{}).(string)
	return v, ok
}
