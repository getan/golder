//go:build windows

package appsession

// processAlive has no portable signal-0 probe on Windows. A lock older than a
// day is reclaimed by lockHolder; a fresher one is respected, which errs toward
// refusing a second writer rather than corrupting a session.
func processAlive(pid int) bool { return pid > 0 }
