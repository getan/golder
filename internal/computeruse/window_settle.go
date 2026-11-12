package computeruse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	windowSettleTimeout  = 3 * time.Second
	windowSettleInterval = 100 * time.Millisecond
	resumeSettleTimeout  = 10 * time.Second
	// OperatorResumeTimeout bounds a GUI or TUI resume without closing the
	// operator connection while a target is still settling.
	OperatorResumeTimeout = 15 * time.Second
)

// transientWindowValidation identifies read-only AX/CG mismatches that can
// occur while a window is being restored or resized. Ambiguity, permissions,
// process identity loss, and unknown failures must never be retried as if the
// selected target were proven.
func transientWindowValidation(err error) bool {
	var d *WindowDiagnostic
	if !errors.As(err, &d) || d == nil {
		return false
	}
	switch d.Reason {
	case WindowBoundsMissing, WindowAXNoWindows, WindowAXGeometry, WindowAXNoMatch:
		return true
	case WindowAXReadFailed:
		return d.AXError == -25204 // kAXErrorCannotComplete
	default:
		return false
	}
}

// Only use this before dispatch, or during a read-only operation. The caller
// must never use it to retry an action whose effect might be unknown.
func noInputValidationError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("computer use window validation did not complete; no input was sent: %w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("computer use window validation did not complete; no input was sent: %w", context.Canceled)
	}
	if transientWindowValidation(err) {
		return fmt.Errorf("%w: the pinned window could not yet be verified; no input was sent: %v", ErrStaleObservation, err)
	}
	return err
}

func retryableValidationFailure(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || transientWindowValidation(err)
}

// waitForWindowValidation repeats only a read-only identity check on the exact
// same target, never input or application launch. A persistent mismatch keeps
// its original diagnostic and all ambiguous targets still fail closed.
func waitForWindowValidation(ctx context.Context, timeout, interval time.Duration, check func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	err := check()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || !transientWindowValidation(err) || timeout <= 0 || interval <= 0 {
		return err
	}
	remaining := timeout - time.Since(started)
	if remaining <= 0 {
		return err
	}
	deadlineAt := started.Add(timeout)
	deadline := time.NewTimer(remaining)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return err
		case <-ticker.C:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !time.Now().Before(deadlineAt) {
				return err
			}
			err = check()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil || !transientWindowValidation(err) {
				return err
			}
		}
	}
}
