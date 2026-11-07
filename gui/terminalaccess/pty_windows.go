//go:build windows

package terminalaccess

import (
	"errors"
	"os"
	"os/exec"

	"github.com/charmbracelet/x/xpty"
)

func configureProcess(*exec.Cmd) {}
func closeParentSlave(xpty.Pty)  {}
func onProcessExit(pty xpty.Pty) { _ = pty.Close() }
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func terminalEOF(err error) bool { return errors.Is(err, os.ErrClosed) }
