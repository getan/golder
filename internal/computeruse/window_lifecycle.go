package computeruse

import "context"

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
	return ok
}

// CloseWindow pauses the session after attempting the request, including when
// cancellation makes its result uncertain. A dialog must be handled by the user.
func (s *Session) CloseWindow(ctx context.Context) error {
	return s.Do(ctx, true, func(ctx context.Context, backend Backend, target Target) error {
		closer, ok := backend.(WindowCloser)
		if !ok {
			return ErrUnavailable
		}
		err := closer.CloseWindow(ctx, target)
		s.mu.Lock()
		if s.status.Active && s.status.Target.ID == target.ID {
			s.status.Paused = true
			s.sequence++
			s.snapshot = nil
			s.frames = nil
		}
		s.mu.Unlock()
		return err
	})
}
