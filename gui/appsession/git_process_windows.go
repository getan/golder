//go:build windows

package appsession

import "os/exec"

// CommandContext retains the platform's normal process cancellation.
func configureOperatorGit(_ *exec.Cmd) {}
