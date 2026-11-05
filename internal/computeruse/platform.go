package computeruse

import "context"

// PlatformBackend exposes an operator-only window picker in addition to the
// target validation required by a session. Implementations must not make the
// picker itself a model-facing tool.
type PlatformBackend interface {
	Backend
	Targets(context.Context) ([]Target, error)
}
