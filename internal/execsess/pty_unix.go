//go:build !windows

package execsess

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Default terminal size for a session PTY: a fixed, conventional window. The
// model does not resize sessions, so any deterministic size works; 24x80
// matches what codex uses for its unified exec.
const (
	ptyRows = 24
	ptyCols = 80
)

// openPTY allocates a pseudo-terminal pair. The master is returned as a file
// the caller reads/writes; the slave is the child's controlling terminal.
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	defer func() {
		if err != nil {
			m.Close()
		}
	}()
	slavePath, err := unlockPTY(m)
	if err != nil {
		return nil, nil, err
	}
	s, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open pty slave %s: %w", slavePath, err)
	}
	if err := unix.IoctlSetWinsize(int(m.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: ptyRows, Col: ptyCols}); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("set pty window size: %w", err)
	}
	return m, s, nil
}
