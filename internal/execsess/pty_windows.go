//go:build windows

package execsess

import (
	"errors"
	"os"
)

// openPTY has no Windows implementation: golder does not build a ConPTY backend,
// so a tty=true request fails with a message pointing at the pipe mode.
func openPTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("pty sessions are not supported on Windows; omit tty (pipe sessions support polling and interrupt)")
}
