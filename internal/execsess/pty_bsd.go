//go:build freebsd || netbsd || openbsd || dragonfly

package execsess

import (
	"fmt"
	"os"
)

// unlockPTY is unimplemented on the BSDs: pigo only targets macOS and Linux
// today, and a tty=true request must fail loudly rather than misbehave.
func unlockPTY(_ *os.File) (string, error) {
	return "", fmt.Errorf("pty sessions are not supported on this platform")
}
