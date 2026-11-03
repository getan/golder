//go:build !windows

package appsession

import "syscall"

// processAlive reports whether pid can receive signal 0.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
