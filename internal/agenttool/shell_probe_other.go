//go:build !windows

package agenttool

import "os/exec"

func configureShellProbe(_ *exec.Cmd) {}
