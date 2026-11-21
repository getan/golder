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
	return inputRefused(ctx, revalidate, staleReason("native_validation_failed", "no input was sent; native validation could not establish safe dispatch"))
}

func inputRefused(ctx context.Context, revalidate func() error, reason error) error {
	if err := ctx.Err(); err != nil {
		return noInputError(err)
	}
	if err := revalidate(); err != nil {
		if ctx.Err() != nil {
			return noInputError(ctx.Err())
		}
		if retryableValidationFailure(err) {
			return noInputError(noInputValidationError(err))
		}
		return noInputError(fmt.Errorf("%w: no input was sent; target revalidation failed: %w", ErrTargetLost, err))
	}
	if err := ctx.Err(); err != nil {
		return noInputError(err)
	}
	return noInputError(reason)
}

// semanticInputRefused is used only when native pre-dispatch checks prove no
// AX request was sent. A failed dispatched request must never use this path.
func semanticInputRefused(ctx context.Context, revalidate func() error) error {
	return semanticInputRefusedStage(ctx, revalidate, "")
}

// Stage names only ever come from fixed native classifier labels.
func semanticInputRefusedStage(ctx context.Context, revalidate func() error, stage string) error {
	message := "no action was sent because the selected control could not be verified or does not support this action; observe again"
	if stage != "" {
		message = "no action was sent at the " + stage + " check; observe again"
	}
	return inputRefused(ctx, revalidate, staleReason("control_not_supported", message))
}
