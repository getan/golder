package computeruse

import (
	"context"
	"fmt"
	"time"
)

// prepareInputFocus restores focus at most once for an already authorized
// input operation, then waits for the OS to confirm it. Checks must prove the
// same pinned window on every pass. It sends no input, never resumes a paused
// session, and must not be called by background preview/observation loops.
// changed tells the caller to recheck the frame after activation.
func prepareInputFocus(ctx context.Context, timeout, interval time.Duration, check func() (bool, error), restore func() error) (changed bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	focused, err := check()
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if focused {
		return false, nil
	}
	if err := restore(); err != nil {
		return true, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		focused, err := check()
		if err != nil {
			return true, err
		}
		if err := ctx.Err(); err != nil {
			return true, err
		}
		if focused {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-deadline.C:
			return true, fmt.Errorf("%w: no input was sent; the selected window did not regain focus", ErrStaleObservation)
		case <-ticker.C:
		}
	}
}
