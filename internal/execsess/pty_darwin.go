//go:build darwin

package execsess

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctlGetPTYName reads the slave device path: macOS's ptmx hands the name
// back through TIOCPTYGNAME instead of a device number.
func ioctlGetPTYName(fd int) (string, error) {
	buf := make([]byte, 128)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return string(buf), nil
}

// unlockPTY names and unlocks the slave device for a fresh /dev/ptmx.
//
// macOS has no TIOCSPTLCK/TIOCGPTN: TIOCPTYGNAME returns the slave path,
// TIOCPTYGRANT publishes it in /dev, and TIOCPTYUNLK clears the exclusive lock.
func unlockPTY(master *os.File) (string, error) {
	name, err := ioctlGetPTYName(int(master.Fd()))
	if err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	return name, nil
}
