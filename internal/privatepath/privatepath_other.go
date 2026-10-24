//go:build !unix

package privatepath

import (
	"io/fs"
	"os"
)

// openNoFollow opens a path for reading. This platform has no O_NOFOLLOW, so
// the open resolves a final link; the checks that follow still describe the
// file that was opened, and callers that must not traverse a link refuse one
// themselves before calling here.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

// ownedByUs reports whether the file belongs to this user. Ownership on this
// platform is an ACL question rather than a numeric owner, and the per-user
// temp and profile directories already scope state to the account, so nothing
// is refused here.
func ownedByUs(fs.FileInfo) bool { return true }

// userSuffix scopes a shared directory name to one account. This platform has
// no process uid to scope by and its temp directory is already per-user.
func userSuffix() string { return "" }
