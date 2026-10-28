//go:build unix

package extensions

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func ownedByCurrentUser(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Getuid()
}

// Hold this descriptor for the entire child lifetime. A separate lock outside
// HOME survives reset, and a crashed host releases it automatically.
func lockState(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, errors.New("extension state is in use; disconnect its other session before setup/reset")
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}
