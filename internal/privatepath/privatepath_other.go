//go:build !unix

package privatepath

import (
	"fmt"
	"io/fs"
	"os"
)

// openNoFollow opens a path for reading after refusing a final symbolic link by
// name. This platform has no O_NOFOLLOW, so the refusal and the open cannot be
// one step; what this closes is the case that matters — a repository planting a
// link inside .golder — without claiming an atomicity it does not have. The
// checks that follow still describe the file that was opened.
func openNoFollow(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: golder never follows one to reach its own state", ErrSymlink)
	}
	return os.Open(path)
}

// ownedByUs reports whether the file belongs to this user. Ownership on this
// platform is an ACL question rather than a numeric owner, and the per-user
// temp and profile directories already scope state to the account, so nothing
// is refused here.
func ownedByUs(fs.FileInfo) bool { return true }

// widerThanPerm reports whether actual grants more than allowed. This platform
// keeps no permission bits: a stat reports every writable file as 0666 and
// access is an ACL question, so comparing bits would refuse the 0600 state
// golder wrote for itself along with everyone else's. What scopes a path here is
// ownedByUs and the per-user directory it lives in.
func widerThanPerm(fs.FileMode, fs.FileMode) bool { return false }

// userSuffix scopes a shared directory name to one account. This platform has
// no process uid to scope by and its temp directory is already per-user.
func userSuffix() string { return "" }
