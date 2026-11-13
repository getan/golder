//go:build !windows

package appsession

import (
	"os"
	"os/exec"
	"syscall"
)

func configureOperatorGit(cmd *exec.Cmd) {
	// Detach SSH/hooks from the GUI's launching terminal. Cancellation must
	// stop descendants too, rather than leaving a remote push running unseen.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
