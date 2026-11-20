package computeruse

import "sync"

// Local GUI policy only. Neither the model nor AX observation can choose or
// initiate a layout. An outstanding choice is bound to a particular grant.
type desktopChoice struct {
	mu       sync.Mutex
	pending  bool
	allowed  bool
	compact  bool
	sequence uint64
}

func (d *desktopChoice) beforeInput(sequence uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.allowed {
		return nil
	}
	d.pending, d.sequence = true, sequence
	return noInputError(staleReason("desktop_layout_choice", "no input was sent; wait for the user's desktop layout choice before requesting a new frame"))
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
	return true
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
