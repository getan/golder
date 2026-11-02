//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !windows

package panecontrol

import "os"

func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
