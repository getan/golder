package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
)

// ToolReviewEvidence is collected by trusted tool code, not decoded from model
// arguments. Facts may contain untrusted page data: they inform review but grant
// no capability. CanSandbox describes the actual target execution envelope.
type ToolReviewEvidence struct {
	Facts      json.RawMessage
	CanSandbox bool
	// These are trusted launch/reuse facts, never model-supplied arguments.
	RuntimeAttested bool
	LaunchRequired  bool
	// Binding is tool-private verification state. It is not reviewer/page data
	// and must never be serialized into the model's execution facts.
	Binding string
}

type toolReviewEvidenceKey struct{}
type boundToolReviewEvidence struct {
	key      [32]byte
	evidence ToolReviewEvidence
}

func toolReviewEvidenceDigest(tool string, args json.RawMessage) [32]byte {
	return sha256.Sum256(append([]byte(strings.ToLower(tool)+"\x00"), args...))
}

func WithToolReviewEvidence(ctx context.Context, tool string, args json.RawMessage, evidence ToolReviewEvidence) context.Context {
	evidence.Facts = append(json.RawMessage(nil), evidence.Facts...)
	return context.WithValue(ctx, toolReviewEvidenceKey{}, boundToolReviewEvidence{toolReviewEvidenceDigest(tool, args), evidence})
}

// ToolReviewEvidenceFromContext refuses inherited evidence for a different call.
func ToolReviewEvidenceFromContext(ctx context.Context, tool string, args json.RawMessage) (ToolReviewEvidence, bool) {
	if ctx == nil {
		return ToolReviewEvidence{}, false
	}
	bound, ok := ctx.Value(toolReviewEvidenceKey{}).(boundToolReviewEvidence)
	if !ok || bound.key != toolReviewEvidenceDigest(tool, args) {
		return ToolReviewEvidence{}, false
	}
	bound.evidence.Facts = append(json.RawMessage(nil), bound.evidence.Facts...)
	return bound.evidence, true
}
