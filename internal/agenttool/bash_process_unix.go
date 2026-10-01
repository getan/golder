//go:build !windows

package agenttool

import (
	"os/exec"
	"syscall"
	"time"
)

const terminationGracePeriod = 50 * time.Millisecond

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	pgid := -pid
	err := syscall.Kill(pgid, syscall.SIGTERM)
	if err != nil {
		return false
	}
	return true
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func killProcess(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
