//go:build windows

package agenttool

import (
	"os/exec"
	"time"
)

const terminationGracePeriod = 50 * time.Millisecond

func configureProcessGroup(_ *exec.Cmd) {}

func terminateProcessGroup(_ int) bool { return false }

func killProcessGroup(_ int) {}

func killProcess(_ int) {}
