//go:build windows

package execsess

import (
	"os/exec"
	"time"
)

// terminationGracePeriod mirrors the unix build; Windows Kill falls back to
// os.Process.Kill, which is immediate.
const terminationGracePeriod = 50 * time.Millisecond

// configureProcessGroup is a no-op on Windows: golder cannot create a process
// group or signal one portably.
func configureProcessGroup(_ *exec.Cmd) {}

// configureTTYProcess is unreachable on Windows: openPTY already fails a
// tty=true request before the process is configured.
func configureTTYProcess(_ *exec.Cmd) {}

func terminateProcessGroup(_ int) bool { return false }

func killProcessGroup(_ int) {}

func interruptProcessGroup(_ int) bool { return false }
