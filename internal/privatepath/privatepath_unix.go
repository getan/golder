//go:build unix

package privatepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// openNoFollow opens a path for reading where a final symbolic link is an
// error rather than something to resolve: O_NOFOLLOW makes the kernel refuse
// it, so there is no window between "is it a link" and "open it".
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: golder never follows one to reach its own state", ErrSymlink)
		}
		return nil, err
	}
	return f, nil
}

// ownedByUs reports whether the file belongs to the effective user.
func ownedByUs(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == ownerUID()
}

// ownerUID is the uid this process runs as. It is a variable so a test on a
// machine where it cannot chown a directory to another account can still stand in
// for one; nothing outside this package sets it.
var ownerUID = os.Geteuid

// userSuffix scopes a shared directory name to one account: "-<uid>".
func userSuffix() string { return "-" + strconv.Itoa(os.Getuid()) }
