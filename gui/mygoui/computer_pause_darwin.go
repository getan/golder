//go:build darwin && cgo

package mygoui

/*
#cgo LDFLAGS: -framework AppKit
#include <stdint.h>
void golderInstallPauseItem(uintptr_t key);
*/
import "C"

import (
	"runtime/cgo"

	"github.com/getan/golder/internal/computeruse"
)

func installComputerPauseItem(s *computeruse.Session) {
	C.golderInstallPauseItem(C.uintptr_t(cgo.NewHandle(s)))
}

// The native item belongs to this session even if another conversation opens.
// Only pause is exposed, never resume, activate or issue input. All callbacks
// are serialized by AppKit; the terminal removal callback releases the handle.
//
//export golderPauseItemState
func golderPauseItemState(key C.uintptr_t, command C.int) C.int {
	h := cgo.Handle(key)
	if command == 2 {
		h.Delete()
		return 0
	}
	s := h.Value().(*computeruse.Session)
	if command == 1 && !s.Status().Paused {
		s.Pause()
	}
	status := s.Status()
	if !status.Active {
		return 0
	}
	if status.Paused {
		return 2
	}
	return 1
}
