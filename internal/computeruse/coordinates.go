package computeruse

import (
	"context"
	"math"
	"time"
	"unicode"
	"unicode/utf8"
)

// FrameKeyBackend and FrameScrollBackend may be offered independently. Both
// must apply the same target, frame and dispatch checks as FrameBoundBackend.
type FrameKeyBackend interface {
	FrameBoundBackend
	PressFrame(context.Context, Target, string, string) error
}

// FrameKeyAvailability narrows the keys a backend can safely implement.
type FrameKeyAvailability interface {
	SupportsFrameKey(string) bool
}

type FrameScrollBackend interface {
	FrameBoundBackend
	ScrollFrame(context.Context, Target, string, float64, float64, int, int) error
}

// FrameTextBackend inserts plain Unicode text only into a positively verified,
// focused, non-secure text control. It must not use the clipboard or fall back
// to an unidentified window/container. Partial dispatch must never be replayed.
type FrameTextBackend interface {
	FrameBoundBackend
	TypeTextFrame(context.Context, Target, string, string) error
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
	if !ok {
		return false
	}
	if available, ok := s.backend.(CaptureAvailability); ok {
		return available.CaptureAvailable()
	}
	return true
}

func (s *Session) SupportsFrameAction(action string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if available, ok := s.backend.(CaptureAvailability); ok && !available.CaptureAvailable() {
		return false
	}
	switch action {
	case "press":
		if _, ok := s.backend.(FrameKeyBackend); !ok {
			return false
		}
		if filter, ok := s.backend.(FrameKeyAvailability); ok {
			for _, key := range frameKeyNames {
				if filter.SupportsFrameKey(key) {
					return true
				}
			}
			return false
		}
		return true
	case "scroll":
		_, ok := s.backend.(FrameScrollBackend)
		return ok
	case "type_text":
		_, ok := s.backend.(FrameTextBackend)
		return ok
	default:
		return false
	}
}

var frameKeyNames = [...]string{"enter", "tab", "escape", "backspace", "delete", "up", "down", "left", "right", "page_up", "page_down", "home", "end", "space"}

func validFrameKey(key string) bool {
	for _, allowed := range frameKeyNames {
		if key == allowed {
			return true
		}
	}
	return false
}

func (s *Session) SupportsFrameKey(key string) bool {
	if s == nil || !validFrameKey(key) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if available, ok := s.backend.(CaptureAvailability); ok && !available.CaptureAvailable() {
		return false
	}
	if _, ok := s.backend.(FrameKeyBackend); !ok {
		return false
	}
	if filter, ok := s.backend.(FrameKeyAvailability); ok {
		return filter.SupportsFrameKey(key)
	}
	return true
}

// ClickFrame consumes a recent, single-use frame lease. Coordinates are
// pixels in that frame, never global screen coordinates or a system handle.
func (s *Session) ClickFrame(ctx context.Context, frameID string, x, y float64) error {
	return s.useFrame(ctx, frameID, x, y, true, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		if isOperatorContext(ctx) {
			if local, ok := backend.(OperatorFrameBackend); ok {
				return local.OperatorClickFrame(ctx, target, lease.token, x/float64(lease.width), y/float64(lease.height))
			}
		}
		return backend.ClickFrame(ctx, target, lease.token, x/float64(lease.width), y/float64(lease.height))
	})
}

// PressFrame permits only a small set of non-modifier keys. The platform must
// also prove the focused control belongs to the target and is not sensitive.
func (s *Session) PressFrame(ctx context.Context, frameID, key string) error {
	if !validFrameKey(key) {
		return ErrNotAllowed
	}
	return s.useFrame(ctx, frameID, 0, 0, false, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		keys, ok := backend.(FrameKeyBackend)
		if !ok {
			return ErrUnavailable
		}
		if filter, ok := backend.(FrameKeyAvailability); ok && !filter.SupportsFrameKey(key) {
			return ErrUnavailable
		}
		return keys.PressFrame(ctx, target, lease.token, key)
	})
}

func validFrameText(text string) bool {
	if text == "" || len(text) > 4096 || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		// Newlines/tabs and other control codes are actions, not plain typing.
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// TypeTextFrame inserts at the caret; unlike semantic fill it does not replace
// the field's entire value and never presses Enter implicitly.
func (s *Session) TypeTextFrame(ctx context.Context, frameID, text string) error {
	if !validFrameText(text) {
		return noInputError(ErrNotAllowed)
	}
	return s.useFrame(ctx, frameID, 0, 0, false, func(ctx context.Context, backend FrameBoundBackend, target Target, lease frameLease) error {
		if isOperatorContext(ctx) {
			if local, ok := backend.(OperatorFrameBackend); ok {
				return local.OperatorTypeTextFrame(ctx, target, lease.token, text)
			}
		}
		typing, ok := backend.(FrameTextBackend)
		if !ok {
			return noInputError(ErrUnavailable)
		}
		return typing.TypeTextFrame(ctx, target, lease.token, text)
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
			return noInputError(ErrStaleObservation)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation(ctx, bound, target, lease)
	})
}
