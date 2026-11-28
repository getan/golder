//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW keeps Windows from allocating a visible console for the
// child. HideWindow is set as well because the console can be painted for a
// frame before the creation flag takes effect.
const createNoWindow = 0x08000000

func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
