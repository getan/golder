// Package privatepath creates and checks the directories and files golder keeps
// one person's state in, and refuses to use anything that is not theirs.
//
// Two failures it exists to make impossible:
//
//   - Following a symlink. A directory named .golder/browser is repository
//     content, so a checkout can contain a link to anywhere; a chmod or a write
//     through it would land on a stranger's file. Every check here opens the
//     path without following a final symlink and inspects the open descriptor,
//     so what is created or tightened is the thing that was named, never what a
//     link pointed at.
//
//   - Using another user's directory. A temp directory is shared by everyone on
//     the machine, so /tmp/golder would be one runtime directory for every
//     person: the first to create it would own the sockets, pid files and locks
//     of all the others, and could pre-create links inside it. Runtime paths are
//     therefore scoped by uid (see RuntimeRoot) and an existing directory whose
//     owner is not this process is refused rather than adopted.
//
// The package is deliberately stdlib-only and small: it is imported by the
// workspace command and by the browser tool, and it must not become a place
// where policy accumulates.
package privatepath

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirMode is what every directory golder creates for itself: owner-only,
	// because these directories hold a session store, a trust table, a browser
	// profile with live logins, and a log of what an agent did.
	DirMode fs.FileMode = 0o700
	// FileMode is owner read/write: what a private file golder writes is.
	FileMode fs.FileMode = 0o600
	// runtimeName is the directory name golder's runtime files live under, inside
	// the per-user runtime root (see RuntimeRoot).
	runtimeName = "golder"
)

var (
	// ErrForeignOwner says the path exists but belongs to another user.
	ErrForeignOwner = errors.New("belongs to another user")
	// ErrSymlink says the path is a symbolic link, which is refused instead of
	// followed.
	ErrSymlink = errors.New("is a symbolic link")
	// ErrNotPrivate says the mode of the path lets someone else read or write it.
	ErrNotPrivate = errors.New("is readable by others")
)

// EnsureDir creates dir as an owner-only (0700) directory, or tightens a
// directory this process already owns to 0700, and refuses a dir that is a
// symlink or another user's.
//
// A directory that already exists wider is tightened rather than refused: it is
// golder's own directory, so 0700 is what it should always have been, and a
// 0755 left behind by an old umask is a leak of everything inside. A directory
// that cannot be tightened is an error that names the mode, because silently
// writing private state into a shared directory is the one outcome worth
// stopping for.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, info, err := openPrivate(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			// A directory this user owns whose mode denies even reading it (0100,
			// 0300, 0000) cannot be opened to be checked, and it is exactly the
			// kind of mode this function exists to repair.
			return tightenUnreadableDir(dir)
		}
		return fmt.Errorf("%s: %w", dir, err)
	}
	defer f.Close()
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if !ownedByUs(info) {
		return fmt.Errorf("%s %w: refusing to create or tighten a directory another account owns", dir, ErrForeignOwner)
	}
	if perm := info.Mode().Perm(); perm != DirMode.Perm() {
		// fchmod on the descriptor this process opened, never a path: a link
		// swapped in after the open cannot redirect it.
		if err := f.Chmod(DirMode); err != nil {
			return fmt.Errorf("%s is mode %04o and must be private (0700) for the state it holds: %w", dir, perm, err)
		}
	}
	return nil
}

// tightenUnreadableDir is the recovery path for a directory this user owns that
// cannot be opened for reading. The checks are made by name and the chmod is by
// name too; the window that usually opens between "is it a link" and "change it"
// is closed here because only the owner of a directory in a sticky shared
// directory can replace it, and the owner is this process.
func tightenUnreadableDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s %w: golder never follows one to reach its own state", dir, ErrSymlink)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if !ownedByUs(info) {
		return fmt.Errorf("%s %w: refusing to create or tighten a directory another account owns", dir, ErrForeignOwner)
	}
	if perm := info.Mode().Perm(); perm != DirMode.Perm() {
		if err := os.Chmod(dir, DirMode); err != nil {
			return fmt.Errorf("%s is mode %04o and must be private (0700) for the state it holds: %w", dir, perm, err)
		}
	}
	return nil
}

// CheckFile reports whether path is a private file this process may trust: not
// a symlink, a regular file, owned by this user where the platform tracks
// owners, and no wider than perm (so 0600 for a file golder itself wrote).
//
// It is the read-side counterpart to EnsureDir, for the files a caller opens
// instead of creating: a metadata record, a credential file. The checks are
// made against an open descriptor, so a file swapped in after the check has to
// win a race inside a directory that is already 0700 and ours.
func CheckFile(path string, perm fs.FileMode) error {
	f, info, err := openPrivate(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	return checkOpened(path, info, perm)
}

// ReadFile reads at most limit bytes of a private file: the same checks as
// CheckFile, and the read, against one open descriptor, so what was checked is
// what was read. A record a repository controls (a metadata file) is the reason
// this is one step rather than two.
func ReadFile(path string, perm fs.FileMode, limit int64) ([]byte, error) {
	f, info, err := openPrivate(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	if err := checkOpened(path, info, perm); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// checkOpened applies the private-file rules to an already-open descriptor.
func checkOpened(path string, info fs.FileInfo, perm fs.FileMode) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if !ownedByUs(info) {
		return fmt.Errorf("%s %w", path, ErrForeignOwner)
	}
	if outside := info.Mode().Perm() &^ perm.Perm(); outside != 0 {
		return fmt.Errorf("%s is mode %04o, wider than the %04o it must be (%w)", path, info.Mode().Perm(), perm.Perm(), ErrNotPrivate)
	}
	return nil
}

// RuntimeRoot is the short, private, per-user directory golder keeps runtime
// files in: <XDG_RUNTIME_DIR>/golder when that variable names an absolute
// directory, else <tmp>/golder-<uid>.
//
// The uid suffix is the point. A temp directory is shared by every account on
// the machine, so a bare "golder" under it would be one directory for all of
// them; naming it after the uid means two accounts never contend for the same
// sockets, pid files or locks, and the ownership check in EnsureDir then refuses
// a directory someone else pre-created under this account's name. $XDG_RUNTIME_DIR
// is per-user by contract, so it needs no suffix — its ownership is still
// checked when it is used.
//
// A relative value in either variable is ignored: a relative runtime directory
// would put sockets under whatever the working directory happens to be.
func RuntimeRoot(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, name := range []string{"XDG_RUNTIME_DIR", "TMPDIR", "TEMP"} {
		dir := strings.TrimSpace(getenv(name))
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if name == "XDG_RUNTIME_DIR" {
			return filepath.Join(dir, runtimeName)
		}
		return filepath.Join(dir, RuntimeName())
	}
	return filepath.Join(os.TempDir(), RuntimeName())
}

// RuntimeName is the directory name golder's runtime files live under inside a
// shared temp directory: "golder-<uid>", or plain "golder" on a platform with
// no per-process user identity to scope by (its temp directory is per-user
// already).
func RuntimeName() string { return runtimeName + userSuffix() }

// openPrivate opens path read-only without following a final symlink and
// returns the descriptor together with the stat of that descriptor, so every
// check describes the file that was opened rather than whatever the name
// pointed at a moment earlier.
func openPrivate(path string) (*os.File, fs.FileInfo, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}
