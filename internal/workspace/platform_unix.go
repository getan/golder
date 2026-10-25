//go:build darwin || linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func supported() error { return nil }

var errLeaseBusy = errors.New("workspace lease is in use")

type stateLease struct {
	file *os.File
	once sync.Once
}

// A socket lock is not sufficient: two explicitly different sockets may name
// the same state directory. Hold this lease before either daemon can clean it.
func lockState(dir string) (*stateLease, error) {
	path := filepath.Join(dir, ".workspace.lock")
	lease, err := lockPath(path)
	if errors.Is(err, errLeaseBusy) {
		return nil, fmt.Errorf("workspace: state directory %q is in use by another workspace; choose a different --state-dir: %w", dir, err)
	}
	return lease, err
}

// lockPath never removes the lock file. In particular a session-name lock lives
// outside the removable session directory: unlinking a locked inode would let a
// concurrent start lock a different file with the same name.
func lockPath(path string) (*stateLease, error) {
	return openLease(path, true)
}

func openLease(path string, create bool) (*stateLease, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if create {
		flags = unix.O_CREAT | unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	}
	fd, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspace: open state lease: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) {
		_ = f.Close()
		return nil, errors.New("workspace: state lease must be a regular file owned by this user")
	}
	if create {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errLeaseBusy
		}
		return nil, err
	}
	return &stateLease{file: f}, nil
}

func (l *stateLease) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() { _ = l.file.Close() })
}

func refusedSocket(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
}

func detachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func openLog(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_APPEND|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) {
		_ = f.Close()
		return nil, errors.New("workspace: log must be a regular file owned by this user")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
