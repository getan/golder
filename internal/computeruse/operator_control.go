package computeruse

import "context"

// Only the local operator frontend can mint this context. No model-facing
// action accepts the key or can resume/enter operator control.
type operatorAccessKey struct{}
type operatorAccess struct {
	session  *Session
	sequence uint64
}

func (s *Session) takeOver() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Active {
		return ErrNotActive
	}
	if !s.status.Capabilities.Control || s.operatorPreview != nil {
		return ErrNotAllowed
	}
	s.status.Paused, s.status.OperatorControl = true, true
	s.sequence++
	s.snapshot, s.frames = nil, nil
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (s *Session) operatorContext(ctx context.Context) (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Active || !s.status.OperatorControl || !s.status.Paused {
		return nil, ErrNotAllowed
	}
	return context.WithValue(ctx, operatorAccessKey{}, operatorAccess{session: s, sequence: s.sequence}), nil
}

// OperatorFrameBackend is never called from a model context. It allows the
// person to address an otherwise opaque canvas while retaining exact-window,
// frame, focus and occlusion checks. It does not broaden Agent capabilities.
type OperatorFrameBackend interface {
	FrameBoundBackend
	OperatorClickFrame(context.Context, Target, string, float64, float64) error
	OperatorTypeTextFrame(context.Context, Target, string, string) error
}

func isOperatorContext(ctx context.Context) bool {
	_, ok := ctx.Value(operatorAccessKey{}).(operatorAccess)
	return ok
}
