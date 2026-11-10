//go:build unix

package extensions

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func ownedByCurrentUser(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Getuid()
}

// Hold this descriptor for the entire child lifetime. A separate lock outside
// HOME survives reset, and a crashed host releases it automatically.
func lockState(path string) (func(), error) {
	return stateLock(nil, path)
}

func lockStateContext(ctx context.Context, path string) (func(), error) {
	return stateLock(ctx, path)
}

func stateLock(ctx context.Context, path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	locked := false
	defer func() {
		if !locked {
			_ = unix.Close(fd)
		}
	}()
	for {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			locked = true
			var once sync.Once
			return func() { once.Do(func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, err
		}
		if ctx == nil {
			return nil, errStateInUse
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
