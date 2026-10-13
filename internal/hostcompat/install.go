package hostcompat

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Installing tuios's harness manifest.
//
// It is the one thing this package writes to disk, and the one thing a host
// needs from golder that a report cannot carry: a report moves a pane's state,
// but only a manifest makes tuios recognise the pane as an agent at all — and
// only a manifest can describe golder's prompts well enough for the Inbox to
// press a key for the person.
//
// The file lives in tuios's own user directory
// ($TUIOS_HARNESS_DIR, else $XDG_CONFIG_HOME/tuios/harnesses), so installing
// needs no root and no cooperation from tuios. Nothing here is required for
// reporting to work; a pane with no manifest still reports state, it is simply
// a pane reporting about a program tuios has never heard of.

//go:embed assets/tuios-golder.toml
var assets embed.FS

// manifestAsset is the path of the manifest inside the embedded assets.
const manifestAsset = "assets/tuios-golder.toml"

// manifestMarker is the comment line carrying the manifest's own version. It
// is what tells an install made by an older golder from a current one, and
// what lets a person tell at a glance whether the file in front of them is
// golder's or one they wrote.
const manifestMarker = "golder-manifest-version:"

// ManifestVersion goes up whenever the shipped manifest changes in a way an
// installed copy has to be replaced to get — a new rule, a reworded one. A
// status check compares it with the marker in the installed file.
const ManifestVersion = 1

// ManifestSource returns the manifest as golder ships it.
func ManifestSource() string {
	data, err := assets.ReadFile(manifestAsset)
	if err != nil {
		// The file is embedded at build time, so this cannot happen in a
		// working build; an empty string is honest about it either way.
		return ""
	}
	return string(data)
}

// ManifestPath is where tuios reads a user manifest from, as this machine has
// it configured: $TUIOS_HARNESS_DIR when set, else $XDG_CONFIG_HOME or
// $HOME. It mirrors internal/harness's UserDir in tuios, which is the only
// authority on the answer.
func ManifestPath(getenv func(string) string) string {
	dir := harnessDir(getenv)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, Harness+".toml")
}

// harnessDir is tuios's user manifest directory.
func harnessDir(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if dir := strings.TrimSpace(getenv("TUIOS_HARNESS_DIR")); dir != "" {
		return dir
	}
	base := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tuios", "harnesses")
}

// ManifestState is what an installed manifest is worth.
type ManifestState int

const (
	// ManifestAbsent: nothing at the path.
	ManifestAbsent ManifestState = iota
	// ManifestCurrent: golder's manifest, at this build's version.
	ManifestCurrent
	// ManifestOutdated: golder's manifest, from an older golder.
	ManifestOutdated
	// ManifestForeign: a file at the path that golder did not write. It is
	// never overwritten or removed: a person editing their own manifest there
	// is exactly the workflow tuios documents.
	ManifestForeign
)

// String is the state in one word, as the diagnostic prints it.
func (s ManifestState) String() string {
	switch s {
	case ManifestCurrent:
		return "installed"
	case ManifestOutdated:
		return "outdated"
	case ManifestForeign:
		return "not golder's"
	default:
		return "not installed"
	}
}

// ManifestStatus reads the manifest tuios would load and says what it is.
// A path that cannot be read is reported as absent with the error, so a caller
// can print why rather than guess.
func ManifestStatus(getenv func(string) string) (state ManifestState, path string, err error) {
	path = ManifestPath(getenv)
	if path == "" {
		return ManifestAbsent, "", errors.New("no home directory to install into")
	}
	data, rerr := os.ReadFile(path) //nolint:gosec // a path under the user's own config dir
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return ManifestAbsent, path, nil
		}
		return ManifestAbsent, path, rerr
	}
	version, ok := manifestVersion(string(data))
	if !ok {
		return ManifestForeign, path, nil
	}
	if version < ManifestVersion {
		return ManifestOutdated, path, nil
	}
	// A version above this build's is a manifest from a newer golder: leave it
	// alone rather than downgrading it, and call it current so nothing nags.
	return ManifestCurrent, path, nil
}

// manifestVersion reads the version marker out of a manifest body.
func manifestVersion(body string) (int, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "#")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		value, ok := strings.CutPrefix(rest, manifestMarker)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// InstallResult says what an install did.
type InstallResult struct {
	// Path is the manifest's location.
	Path string
	// Changed is false when the file was already current, which is the common
	// case: installing twice is not an error and does not rewrite the file.
	Changed bool
	// State is what was there before the call.
	State ManifestState
}

// Install writes golder's manifest into tuios's user directory, and does
// nothing when the file is already current.
//
// A file at the path that golder did not write is refused, never overwritten:
// it is the person's, and tuios documents editing these by hand. A symlink is
// followed the way tuios follows one when it reads the directory, so a
// dotfile-managed config installs into the file the link points at.
func Install(getenv func(string) string) (InstallResult, error) {
	state, path, err := ManifestStatus(getenv)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return InstallResult{Path: path, State: state}, err
	}
	switch state {
	case ManifestCurrent:
		return InstallResult{Path: path, State: state}, nil
	case ManifestForeign:
		return InstallResult{Path: path, State: state},
			fmt.Errorf("%s exists and golder did not write it; move it aside to install golder's, or keep your own", path)
	}
	if path == "" {
		return InstallResult{}, errors.New("no config directory to install into")
	}
	// The file to write is the one a symlink at path names, not the link: a
	// dotfile-managed config points the manifest at the dotfiles repository,
	// and replacing the link with a plain file would quietly take that config
	// out of the person's hands.
	writePath := resolveWritePath(path)
	if err := os.MkdirAll(filepath.Dir(writePath), 0o755); err != nil {
		return InstallResult{Path: path, State: state}, err
	}
	// The link's own directory may not exist yet either (the tuios config dir
	// is created on a first install).
	if dir := filepath.Dir(path); dir != filepath.Dir(writePath) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return InstallResult{Path: path, State: state}, err
		}
	}
	if err := writeFileAtomic(writePath, ManifestSource()); err != nil {
		return InstallResult{Path: path, State: state}, err
	}
	return InstallResult{Path: path, Changed: true, State: state}, nil
}

// maxSymlinkDepth bounds following a chain of links. A chain longer than this
// is a loop or a mistake, and either way there is nothing to write.
const maxSymlinkDepth = 8

// resolveWritePath returns the path an install should write: path itself, or
// the file it is a symlink to — including a symlink whose target does not exist
// yet, which is exactly what a freshly added dotfile-managed entry looks like.
func resolveWritePath(path string) string {
	for range maxSymlinkDepth {
		target, err := os.Readlink(path)
		if err != nil {
			// Not a link, which is the ordinary case.
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = filepath.Clean(target)
	}
	return path
}

// Uninstall removes golder's manifest, and only golder's: the marker has to be
// in the file. It reports whether anything was removed.
func Uninstall(getenv func(string) string) (path string, removed bool, err error) {
	state, path, err := ManifestStatus(getenv)
	if err != nil {
		return path, false, err
	}
	if state != ManifestCurrent && state != ManifestOutdated {
		return path, false, nil
	}
	if err := os.Remove(path); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// writeFileAtomic writes data to path by way of a temporary file in the same
// directory, so a reader never sees a half-written manifest and a failure
// leaves the old file in place. path is the file itself: following a symlink is
// resolveWritePath's job, done before this so a dangling link is written
// through rather than replaced.
func writeFileAtomic(path string, data string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".golder-manifest-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort: the rename below removes the need for this, and a
		// leftover temp file is not worth failing an install over.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
