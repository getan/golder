//go:build linux

package execsess

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// unlockPTY unlocks a fresh /dev/ptmx (TIOCSPTLCK 0) and returns its slave
// device path derived from the device number (TIOCGPTN).
func unlockPTY(master *os.File) (string, error) {
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}
