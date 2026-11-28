//go:build !windows

package browserctl

import (
	"fmt"
	"os"
	"path/filepath"
)

// checkSocket refuses a path that is not an owner-only Unix socket in a
// directory other users cannot write. The browser accepts typed input, so the
// socket's permissions are the whole authorization.
func checkSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("control socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("control socket %s is accessible by other users (%04o); it must be owner-only", path, perm)
	}
	dir := filepath.Dir(path)
	if di, err := os.Stat(dir); err == nil {
		// A sticky directory such as /tmp is acceptable only because the socket
		// itself is owner-only; a plain world-writable one lets someone swap it.
		if di.Mode().Perm()&0o002 != 0 && di.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("control socket directory %s is world-writable", dir)
		}
	}
	return nil
}
