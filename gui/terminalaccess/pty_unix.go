//go:build darwin || linux

package terminalaccess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/charmbracelet/x/xpty"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

func closeParentSlave(pty xpty.Pty) {
	if p, ok := pty.(*xpty.UnixPty); ok {
		_ = p.Slave().Close()
	}
}

func onProcessExit(xpty.Pty) {}

func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
	}
}

func terminalEOF(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)
}
