package computeruse

import (
	"sync"
	"time"
)

// Local GUI policy only. Neither the model nor AX observation can choose or
// initiate a layout. An outstanding choice is bound to a particular grant.
type desktopChoice struct {
	mu       sync.Mutex
	pending  bool
	allowed  bool
	compact  bool
	sequence uint64
	deadline time.Time
	expired  bool
}

func (d *desktopChoice) beforeInput(sequence uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.allowed {
		return nil
	}
	// The GUI can be minimized, hidden or on another Space, in which case its
	// sheet cannot appear. Never block automation forever: after the deadline,
	// degrade to keeping the layout and let the existing focus checks decide.
	if d.pending && sequence == d.sequence && time.Now().Before(d.deadline) {
		return noInputError(staleReason("desktop_layout_choice", "no input was sent; the operator is choosing a layout. Do not retry or request another frame until they respond"))
	}
	if d.pending && sequence != d.sequence {
		d.pending = false
	}
	d.pending, d.sequence, d.deadline = true, sequence, time.Now().Add(20*time.Second)
	return noInputError(staleReason("desktop_layout_choice", "no input was sent; the operator is choosing a layout. Do not retry or request another frame until they respond"))
}

func (d *desktopChoice) expire(sequence uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pending || d.sequence != sequence || time.Now().Before(d.deadline) {
		return false
	}
	d.pending, d.allowed, d.compact = false, true, false
	d.expired = true
	return true
}

func (d *desktopChoice) requested(sequence uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending && d.sequence == sequence
}

func (d *desktopChoice) finish(sequence uint64, compact bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pending || sequence != d.sequence {
		return false
	}
	d.pending, d.allowed, d.compact = false, true, compact
	d.expired = false
	return true
}

// DesktopNotice explains a silent fallback where the UI can still show it.
func (s *Session) DesktopNotice() string {
	s.mu.Lock()
	d := s.desktop
	s.mu.Unlock()
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.expired {
		return "布局选择超时，已保持原布局；按住 Option 可重新选择。Layout choice timed out; the original layout was kept. Hold Option to choose again."
	}
	return ""
}

// CompactDesktop is a presentation hint, not a control capability.
func (s *Session) CompactDesktop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desktop == nil || !s.status.Active {
		return false
	}
	s.desktop.mu.Lock()
	defer s.desktop.mu.Unlock()
	return s.desktop.compact
}
