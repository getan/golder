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

// configurePipeProcess puts the child in its own session and process group so a
// kill or interrupt reaches forked children too (a `sleep 30 | cat` pipeline
// cannot outlive its shell), and so the child has no controlling terminal.
//
// The terminal detachment is as important as the signal group. A pipe session
// captures stdout/stderr, but a program that opens /dev/tty — ssh's host-key
// confirmation, sudo's password prompt, git's credential prompt — would bypass
// those pipes and write straight into golder's terminal. In the TUI that
// corrupts the raw-mode screen and leaves the user typing "yes" into a prompt
// the tool cannot see. With no controlling terminal the open fails, the
// program reports its error on the captured stderr, and the session ends
// instead of taking over the screen. The session leader's process group id
// equals its pid, so process-group signals still reach forked children.
func configurePipeProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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
