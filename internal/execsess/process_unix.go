//go:build !windows

package execsess

import (
	"os/exec"
	"syscall"
	"time"
)

// terminationGracePeriod is how long Kill waits after SIGTERM before sending
// SIGKILL to the process group.
const terminationGracePeriod = 50 * time.Millisecond

// configureProcessGroup puts the child in its own process group so a kill or
// interrupt reaches forked children too (a `sleep 30 | cat` pipeline cannot
// outlive its shell).
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// configureTTYProcess gives a tty session its own session and makes the slave
// fd (the child's stdin) its controlling terminal. The session leader's process
// group id equals its pid, so process-group signals below still reach forked
// children.
func configureTTYProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

// terminateProcessGroup sends SIGTERM to the process group. It reports whether
// the signal was delivered.
func terminateProcessGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(-pid, syscall.SIGTERM) == nil
}

// killProcessGroup sends SIGKILL to the process group.
func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// interruptProcessGroup sends SIGINT (Ctrl-C) to the process group. It reports
// whether the signal was delivered.
func interruptProcessGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(-pid, syscall.SIGINT) == nil
}
