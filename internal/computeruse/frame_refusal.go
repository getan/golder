package computeruse

import (
	"context"
	"fmt"
)

// frameInputRefused is only for a platform path that proves it sent no input.
// If the pinned target still validates, the consumed frame is merely stale:
// the caller can observe again without an operator having to resume. A changed
// or unprovable target remains target loss and pauses the session as before.
func frameInputRefused(ctx context.Context, revalidate func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := revalidate(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: no input was sent; target revalidation failed: %w", ErrTargetLost, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: no input was sent because focus, visibility, geometry or the hit target could not be verified; capture a new frame or use an authorized semantic action", ErrStaleObservation)
}
