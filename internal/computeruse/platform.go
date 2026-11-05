package computeruse

import "context"

// PlatformBackend exposes an operator-only window picker in addition to the
// target validation required by a session. Implementations must not make the
// picker itself a model-facing tool.
type PlatformBackend interface {
	Backend
	Targets(context.Context) ([]Target, error)
}

// OperatorPreviewBackend supports a second, operator-only source selection.
// The candidate is never exposed through the model-facing Session before the
// person explicitly confirms that it matches the already selected target.
type OperatorPreviewBackend interface {
	PrepareOperatorPreview(context.Context, Target) (OperatorPreview, error)
	ConfirmOperatorPreview(context.Context, Target, string) error
}

// OperatorPreviewDiscarder permits an explicit semantic-only fallback.
type OperatorPreviewDiscarder interface {
	DiscardOperatorPreview(context.Context, Target, string) error
}

type OperatorPreview struct {
	ID  string
	PNG []byte
}
