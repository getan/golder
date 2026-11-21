package computeruse

import (
	"context"
	"errors"
	"time"
)

var errRaiseNotVisible = errors.New("targeted raise did not clear the desktop activation guard")

// After one targeted raise, wait briefly for an eligible activation or an
// already focused window. A completed or refused activation is never retried.
func afterPinnedRaise(ctx context.Context, budget, interval time.Duration, check func() (done bool, err error)) error {
	deadline := time.Now().Add(budget)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return errRaiseNotVisible
		}
		if done, err := check(); done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
