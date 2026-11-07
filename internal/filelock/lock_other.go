//go:build !unix && !windows

package filelock

import "errors"

func Acquire(string) (func(), error) {
	return nil, errors.New("cross-process state locking is unavailable on this platform")
}
