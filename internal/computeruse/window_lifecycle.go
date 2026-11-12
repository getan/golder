package computeruse

import (
	"context"
	"fmt"
)

// WindowCloser requests closure of the already authorized window, not its app.
// A successful request does not prove that the window actually closed.
type WindowCloser interface {
	CloseWindow(context.Context, Target) error
}

func (s *Session) SupportsCloseWindow() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.backend.(WindowCloser)
	return ok && !s.closeRequested
}

// CloseWindow attempts one request on the pinned window. Subsequent reads may
// inspect that same window, but an unknown or successful request is never replayed.
// A different window or an unprovable target remains outside this session.
func (s *Session) CloseWindow(ctx context.Context) error {
	return s.Do(ctx, true, func(ctx context.Context, backend Backend, target Target) error {
		closer, ok := backend.(WindowCloser)
		if !ok {
			return fmt.Errorf("%w: %w", ErrNoInput, ErrUnavailable)
		}
		s.mu.Lock()
		if !s.status.Active || s.status.Paused || s.status.Target.ID != target.ID || ctx.Err() != nil {
			s.mu.Unlock()
			return fmt.Errorf("%w: %w", ErrNoInput, ErrStaleObservation)
		}
		if s.closeRequested {
			s.mu.Unlock()
			return fmt.Errorf("%w: %w", ErrNoInput, ErrClosePending)
		}
		s.closeRequested = true
		s.sequence++
		s.snapshot = nil
		s.frames = nil
		s.mu.Unlock()
		return closer.CloseWindow(ctx, target)
	})
}
