//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockWriterFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errWriterLocked
	}
	return err
}

func unlockWriterFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
