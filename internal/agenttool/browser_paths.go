package agenttool

// Where a browser pane keeps its state.
//
// Two facts drive this file. A Chromium profile holds cookies and logins, so it
// must be owner-only, not the 0755 directory a first draft created. And a
// control endpoint is a Unix socket, whose path is short by construction: 104
// bytes on macOS and 108 on Linux, counting the terminating NUL. A workspace
// near the limit would otherwise make the browser fail to bind *after* golder
// had already opened the pane, with the daemon reporting "invalid argument" and
// no way to tell why. So the socket stays in .golder/browser when it fits, and
// otherwise moves to a short, private, per-project runtime directory that
// .golder/browser records in metadata — which is what discovery reads, so a
// browser started by an earlier session is still found.
//
// Every one of those paths is more dangerous than it looks, because
// .golder/browser is repository content and the runtime directory lives under a
// shared temp directory:
//
//   - A checkout can contain ".golder" or ".golder/browser" as a symlink to
//     anywhere, so neither is followed: state is written only into a real
//     directory (see ensureBrowserDir), and the profile directory inside it is
//     created the same way.
//   - A runtime directory in a world-writable temp dir can be pre-created by
//     another account, so its name is scoped by uid (privatepath.RuntimeRoot)
//     and a directory owned by someone else is refused rather than used.
//   - A metadata record can be a symlink to a private file elsewhere, so it is
//     read only as a regular file this user owns, and the record itself is
//     validated (checkBrowserRecord): a name that is not a plain file name, a
//     socket golder could not have created here, or a profile that is not this
//     browser's are all refused. That matters most for close, which deletes the
//     record the name points at.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/privatepath"
)

const (
	// unixSocketPathLimit is one byte under the smallest sockaddr_un path limit
	// golder runs on (104 on macOS, including the NUL), so the same paths work
	// everywhere without a platform file.
	unixSocketPathLimit = 103
	// socketSuffixRoom is the space a second concurrent instance needs for its
	// "-a1b2c3d4" suffix.
	socketSuffixRoom = len("-a1b2c3d4")
	// maxBrowserMetadata bounds one metadata file.
	maxBrowserMetadata = 8 << 10
)

var errBrowserName = errors.New("browser name must be a plain file name")

// checkBrowserName refuses a name that would escape the state directory or
// produce an unparseable socket name.
func checkBrowserName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") {
		return fmt.Errorf("%w: %q", errBrowserName, name)
	}
	return nil
}

// browserStateDir is the workspace's browser state directory: profiles, the
// sockets that fit, and the metadata that makes a runtime socket discoverable.
func browserStateDir(cwd string) string { return filepath.Join(cwd, ".golder", "browser") }

// ensureBrowserDir creates dir — a .golder/browser state directory or a runtime
// directory — as a real, owner-only directory, and refuses to reach it through
// a symlink.
//
// Both components matter. ".golder" and ".golder/browser" are inside a checkout,
// so a repository can contain either as a link to a private directory elsewhere;
// writing a Chromium profile through that link would put the person's cookies
// wherever the link points, and chmod through it would change a stranger's
// directory mode. The parent of a runtime directory is checked for the same
// reason: <tmp>/golder-<uid> is the one level a pre-created link could replace.
func ensureBrowserDir(dir string) error {
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("browser state %s is a symbolic link, and golder keeps no browser state through one", path)
		case err != nil && !os.IsNotExist(err):
			return fmt.Errorf("browser state %s: %w", path, err)
		}
	}
	if err := privatepath.EnsureDir(dir); err != nil {
		return fmt.Errorf("browser state directory: %w", err)
	}
	return nil
}

// browserProfileDir is the persistent Chromium profile for name.
func browserProfileDir(cwd, name string) string {
	return filepath.Join(browserStateDir(cwd), name+".profile")
}

// browserMetadataPath is where a browser pane's identity and endpoint are
// recorded, next to the profiles it owns.
func browserMetadataPath(cwd, name string) string {
	return filepath.Join(browserStateDir(cwd), name+".browser.json")
}

// browserMetadata is what one browser pane leaves behind. It exists for
// discovery across sessions (a runtime socket is otherwise unfindable) and for
// close (the exact pane id, program and browserId the pane was started with).
// One record per browser name, so the newest launch wins; it holds no secret
// and the profile and the socket's own permissions stay the access control.
type browserMetadata struct {
	Name      string    `json:"name"`
	Socket    string    `json:"socket"`
	Profile   string    `json:"profile,omitempty"`
	WindowID  string    `json:"window_id,omitempty"`
	Program   string    `json:"program,omitempty"`
	BrowserID string    `json:"browser_id,omitempty"`
	Embedded  bool      `json:"embedded,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func readBrowserMetadata(path string) (browserMetadata, error) {
	// A record is repository content, so it is read as a regular file this user
	// owns that nobody else can read, and the check and the read are one open:
	// a symlink here would otherwise hand a checkout the contents of any file on
	// the machine.
	raw, err := privatepath.ReadFile(path, privatepath.FileMode, maxBrowserMetadata)
	if err != nil {
		return browserMetadata{}, err
	}
	var meta browserMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return browserMetadata{}, fmt.Errorf("%s: %w", path, err)
	}
	return meta, nil
}

// checkBrowserRecord reports whether a decoded record is one golder could have
// written for this workspace. Reading is not trust: the file is repository
// content, so a record may claim any name, socket or profile it likes, and close
// acts on the name. A record has to name a plain browser name, a socket in a
// directory golder could have put one in, and — when it names a profile at all —
// the profile golder gives that browser.
func checkBrowserRecord(root string, meta browserMetadata) error {
	if err := checkBrowserName(meta.Name); err != nil {
		return fmt.Errorf("browser record names %q: %w", meta.Name, err)
	}
	if !validBrowserSocketLocation(root, meta.Socket) {
		return fmt.Errorf("browser record %q names %q, which is not a socket golder could have created for this workspace", meta.Name, meta.Socket)
	}
	if meta.Profile != "" && canonicalBrowserSocket(meta.Profile) != canonicalBrowserSocket(browserProfileDir(root, meta.Name)) {
		return fmt.Errorf("browser record %q names profile %q, which is not %q", meta.Name, meta.Profile, browserProfileDir(root, meta.Name))
	}
	return nil
}

// writeBrowserMetadata replaces one record atomically, so a reader never sees a
// half-written identity. A failure is reported to the caller, which treats the
// record as best effort: the pane still works, it is just harder to find later.
func writeBrowserMetadata(path string, meta browserMetadata) error {
	if err := checkBrowserName(meta.Name); err != nil {
		return err
	}
	meta.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := ensureBrowserDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".browser-metadata-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(privatepath.FileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// removeBrowserMetadata drops the record for a browser name, so a later
// session does not probe an endpoint that is gone.
func removeBrowserMetadata(cwd, name string) {
	// The name usually came from a record this workspace did not write, so it is
	// checked again here: "../../id_rsa.browser.json" must not delete anything
	// outside the state directory.
	if err := checkBrowserName(name); err != nil {
		return
	}
	_ = os.Remove(browserMetadataPath(cwd, name))
}

// browserRuntimeCandidates lists the short private directories a project's
// sockets may live in, in preference order. It touches nothing: a caller that
// wants a directory creates it with ensureBrowserRuntimeDir.
//
// They are all under golder's per-user runtime root, so the directory name
// carries the uid whenever the root is a shared temp directory: two accounts
// never contend for one socket directory, and the ownership check in
// ensureBrowserDir refuses a directory the other account pre-created. The
// second candidate is the temp directory itself, used when $XDG_RUNTIME_DIR is
// set but unusable (a full or stale mount); the project key keeps two projects
// apart inside either.
func browserRuntimeCandidates(cwd string) []string {
	name := "browser-" + browserProjectKey(cwd)
	var out []string
	seen := map[string]bool{}
	for _, root := range []string{
		privatepath.RuntimeRoot(os.Getenv),
		filepath.Join(os.TempDir(), privatepath.RuntimeName()),
	} {
		if root == "" {
			continue
		}
		dir := filepath.Join(root, name)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// browserProjectKey is a stable short digest of the workspace path, so two
// projects in the same temp dir never share a socket directory and a session
// started later computes the same one.
func browserProjectKey(cwd string) string {
	path := cwd
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		path = real
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
}

// ensureBrowserRuntimeDir creates a private directory and refuses one that is
// not private and ours. In a world-writable temp dir this is the check that
// stops a pre-created directory — or a symlink to one — from becoming the
// socket endpoint.
func ensureBrowserRuntimeDir(dir string) error { return ensureBrowserDir(dir) }

// browserControlSocketPath resolves the control socket for a browser named
// name. It prefers .golder/browser so the workspace alone describes the
// session, and falls back to a private runtime directory when the workspace
// prefix would push the path past the Unix limit. When no candidate fits the
// error says exactly what to shorten instead of letting bind fail later.
func browserControlSocketPath(cwd, name string) (string, error) {
	if err := checkBrowserName(name); err != nil {
		return "", err
	}
	file := name + ".sock"
	preferred := filepath.Join(browserStateDir(cwd), file)
	if len(preferred)+socketSuffixRoom <= unixSocketPathLimit {
		return preferred, nil
	}
	var tried []string
	var failed error
	for _, dir := range browserRuntimeCandidates(cwd) {
		path := filepath.Join(dir, file)
		if len(path)+socketSuffixRoom > unixSocketPathLimit {
			tried = append(tried, path)
			continue
		}
		if err := ensureBrowserRuntimeDir(dir); err != nil {
			// An unusable runtime root (unwritable, a stale mount, a directory
			// another account owns) is not a reason to give up while another
			// short private directory is still available.
			failed = err
			continue
		}
		return path, nil
	}
	if failed != nil {
		return "", fmt.Errorf("cannot create a private browser runtime directory: %w", failed)
	}
	return "", fmt.Errorf("cannot place a browser control socket: %s is %d bytes and %s is the most a Unix socket path allows on this system; "+
		"shorten %q or move the workspace to a shorter path (golder will not open a pane whose control endpoint cannot bind)",
		strings.Join(tried, " and "), len(preferred), fmt.Sprintf("%d bytes", unixSocketPathLimit), name)
}

// validBrowserSocketLocation reports whether a recorded socket is one golder
// could have created for this workspace: directly inside .golder/browser or
// inside the project's runtime directory. A metadata file is repository
// content, so it must not be able to point discovery or close at an arbitrary
// endpoint elsewhere on the machine.
func validBrowserSocketLocation(root, socket string) bool {
	socket = strings.TrimSpace(socket)
	if socket == "" || !filepath.IsAbs(socket) {
		return false
	}
	dir := canonicalBrowserSocket(filepath.Dir(socket))
	if dir == canonicalBrowserSocket(browserStateDir(root)) {
		return true
	}
	for _, candidate := range browserRuntimeCandidates(root) {
		if dir == canonicalBrowserSocket(candidate) {
			return true
		}
	}
	return false
}
