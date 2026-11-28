//go:build windows

package agenttool

import (
	"os/exec"
	"syscall"
)

// configureShellProbe keeps the short liveness probe from flashing a console
// window when golder runs as a GUI application.
func configureShellProbe(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
