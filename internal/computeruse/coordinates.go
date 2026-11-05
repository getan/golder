package computeruse

import (
	"context"
	"math"
	"time"
)

// FrameKeyBackend and FrameScrollBackend may be offered independently. Both
// must apply the same native target and frame checks as FrameBoundBackend.
type FrameKeyBackend interface {
	FrameBoundBackend
	PressFrame(context.Context, Target, string, string) error
}

type FrameScrollBackend interface {
	FrameBoundBackend
	ScrollFrame(context.Context, Target, string, float64, float64, int, int) error
}

// SupportsCoordinates is true only when the backend can bind and revalidate
// a captured frame through its native input safety checks.
func (s *Session) SupportsCoordinates() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.backend.(FrameBoundBackend)
	return ok
}

func (s *Session) SupportsFrameAction(action string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case "press":
		_, ok := s.backend.(FrameKeyBackend)
		return ok
	case "scroll":
		_, ok := s.backend.(FrameScrollBackend)
		return ok
	default:
		return false
	}
}

// ClickFrame consumes a recent, single-use frame lease. Coordinates are
// pixels in that frame, never global screen coordinates or a system handle.
func (s *Session) ClickFrame(ctx context.Context, frameID string, x, y float64) error {
	return s.useFrame(ctx, frameID, x, y, true, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		return backend.ClickFrame(ctx, target, lease.token, x/float64(lease.width), y/float64(lease.height))
	})
}

// PressFrame permits only a small set of non-modifier keys. The platform must
// also prove the focused control belongs to the target and is not sensitive.
func (s *Session) PressFrame(ctx context.Context, frameID, key string) error {
	switch key {
	case "enter", "tab", "escape", "backspace", "delete", "up", "down", "left", "right", "page_up", "page_down", "home", "end", "space":
	default:
		return ErrNotAllowed
	}
	return s.useFrame(ctx, frameID, 0, 0, false, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		keys, ok := backend.(FrameKeyBackend)
		if !ok {
			return ErrUnavailable
		}
		return keys.PressFrame(ctx, target, lease.token, key)
	})
}

// ScrollFrame applies a bounded number of lines at an authorized frame point.
func (s *Session) ScrollFrame(ctx context.Context, frameID string, x, y float64, dx, dy int) error {
	if dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
		return ErrNotAllowed
	}
	return s.useFrame(ctx, frameID, x, y, true, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		scroll, ok := backend.(FrameScrollBackend)
		if !ok {
			return ErrUnavailable
		}
		return scroll.ScrollFrame(ctx, target, lease.token, x/float64(lease.width), y/float64(lease.height), dx, dy)
	})
}

func (s *Session) useFrame(ctx context.Context, frameID string, x, y float64, point bool, operation func(context.Context, FrameBoundBackend, Target, frameLease) error) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return ErrNotAllowed
	}
	return s.Do(ctx, true, func(ctx context.Context, backend Backend, target Target) error {
		bound, ok := backend.(FrameBoundBackend)
		if !ok {
			return ErrUnavailable
		}
		s.mu.Lock()
		lease, exists := s.frames[frameID]
		delete(s.frames, frameID)
		valid := exists && frameID != "" && lease.targetID == target.ID && lease.sequence == s.sequence &&
			time.Since(lease.created) <= frameLeaseLifetime && (!point || (x >= 0 && y >= 0 && x < float64(lease.width) && y < float64(lease.height)))
		s.mu.Unlock()
		if !valid {
			return ErrStaleObservation
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation(ctx, bound, target, lease)
	})
}
