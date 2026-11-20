//go:build darwin && cgo

package computeruse

/*
#include <stdint.h>
*/
import "C"

import "runtime/cgo"

type layoutGrant struct {
	s      *Session
	choice *desktopChoice
	target string
}

// UI callbacks are serialized on AppKit's main queue. A positive ticket names
// the particular request; a sheet opened before Pause cannot authorize Resume.
// -1: session ended, 0: no live request. Commands: poll, keep, tile, release.
//export golderDesktopChoice
func golderDesktopChoice(key C.uintptr_t, command C.int, ticket C.int64_t) C.int64_t {
	h := cgo.Handle(key)
	if command == 3 { h.Delete(); return -1 }
	g := h.Value().(layoutGrant)
	s := g.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Active || s.status.Target.ID != g.target || s.desktop != g.choice { return -1 }
	if s.status.Paused || !s.status.Capabilities.Control || !g.choice.requested(s.sequence) { return 0 }
	current := C.int64_t(s.sequence+1)
	if command == 0 { return current }
	if ticket != current { return 0 }
	if command == 4 {
		s.status.Paused, s.status.OperatorControl = true, false
	} else if (command != 1 && command != 2) || !g.choice.finish(s.sequence, command == 2) {
		return 0
	}
	s.sequence++
	s.frames, s.snapshot = nil, nil
	if s.cancel != nil { s.cancel() }
	return current
}
