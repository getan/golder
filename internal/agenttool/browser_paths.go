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
	f, err := os.Open(path)
	if err != nil {
		return browserMetadata{}, err
	}
	defer f.Close()
	raw := make([]byte, maxBrowserMetadata)
	n, err := f.Read(raw)
	if err != nil && n == 0 {
		return browserMetadata{}, err
	}
	var meta browserMetadata
	if err := json.Unmarshal(raw[:n], &meta); err != nil {
		return browserMetadata{}, fmt.Errorf("%s: %w", path, err)
	}
	return meta, nil
}

// writeBrowserMetadata replaces one record atomically, so a reader never sees a
// half-written identity. A failure is reported to the caller, which treats the
// record as best effort: the pane still works, it is just harder to find later.
func writeBrowserMetadata(path string, meta browserMetadata) error {
	meta.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".browser-metadata-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
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
	_ = os.Remove(browserMetadataPath(cwd, name))
}

// browserRuntimeCandidates lists the short private directories a project's
// sockets may live in, in preference order. It touches nothing: a caller that
// wants a directory creates it with ensureBrowserRuntimeDir.
func browserRuntimeCandidates(cwd string) []string {
	bases := make([]string, 0, 2)
	if dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); dir != "" {
		bases = append(bases, dir)
	}
	bases = append(bases, os.TempDir())
	out := make([]string, 0, len(bases))
	for _, base := range bases {
		if base == "" {
			continue
		}
		out = append(out, filepath.Join(base, "golder-browser-"+browserProjectKey(cwd)))
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
// stops a pre-created directory from becoming the socket endpoint.
func ensureBrowserRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("browser runtime directory %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("browser runtime directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("browser runtime directory %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("browser runtime directory %s is not private and cannot be made so: %w", dir, err)
	}
	return nil
}

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
	for _, dir := range browserRuntimeCandidates(cwd) {
		path := filepath.Join(dir, file)
		if len(path)+socketSuffixRoom > unixSocketPathLimit {
			tried = append(tried, path)
			continue
		}
		if err := ensureBrowserRuntimeDir(dir); err != nil {
			return "", err
		}
		return path, nil
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
