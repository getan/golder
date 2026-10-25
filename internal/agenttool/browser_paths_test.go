package agenttool

// Path safety for the browser pane, tested where it is decided rather than
// through a launched browser: .golder/browser is repository content, and the
// runtime directory sits in a shared temp directory, so both can be created by
// someone else. Each test below states one thing golder refuses to do, and what
// must be left untouched when it refuses.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/privatepath"
)

// deepWorkspace is a workspace path long enough that a socket inside
// .golder/browser cannot fit the Unix limit, so the runtime directory is used.
func deepWorkspace(t *testing.T) string {
	t.Helper()
	deep := filepath.Join(shortRoot(t), strings.Repeat("d", 70), "project")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	return deep
}

// tinyRoot is a temporary directory short enough that a socket path inside it is
// nowhere near the Unix limit. A runtime root inherits the length of $TMPDIR, and
// the ordinary temp directory on macOS (/var/folders/…) spends most of a socket
// path's budget on its own name.
func tinyRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gb")
	if err != nil {
		// No usable /tmp: fall back to the ordinary temp directory. The test's own
		// length assertions then say whether that directory is short enough.
		if dir, err = os.MkdirTemp("", "gb"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// A checkout can contain .golder as a symlink. Writing browser state through it
// would put a profile full of logins wherever the link points, and chmod through
// it would change the mode of a directory golder does not own.
func TestBrowserStateDirectoryRefusesASymlinkedGolderDir(t *testing.T) {
	root, outside := shortRoot(t), shortRoot(t)
	target := filepath.Join(outside, "grabbed")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".golder")); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}

	err := writeBrowserMetadata(browserMetadataPath(root, "browser"), browserMetadata{
		Name: "browser", Socket: filepath.Join(root, ".golder", "browser", "browser.sock"),
	})
	if err == nil {
		t.Fatal("wrote a record through a symlinked .golder")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v, want it to say the directory is a link", err)
	}
	info, statErr := os.Lstat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the link's target gained %v, want nothing", entries)
	}
}

// The same rule one level down: .golder is real, .golder/browser is the link.
func TestBrowserStateDirectoryRefusesASymlinkedBrowserDir(t *testing.T) {
	root := shortRoot(t)
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".golder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, browserStateDir(root)); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}
	if err := writeBrowserMetadata(browserMetadataPath(root, "browser"), browserMetadata{
		Name: "browser", Socket: filepath.Join(browserStateDir(root), "browser.sock"),
	}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err = %v, want a refusal naming the link", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
}

// A profile holds cookies and logins, so it is never created through a link a
// checkout could contain either.
func TestBrowserProfileRefusesASymlink(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	if err := os.MkdirAll(browserStateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, browserProfileDir(root, "browser")); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}
	if _, err := browserPaneSpec(root, "browser", "https://example.com"); err == nil ||
		!strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("err = %v, want a refusal of the linked profile directory", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
}

// A metadata record is a file in the repository: a link named
// "*.browser.json" would otherwise let a checkout hand discovery — and close —
// the contents of any private file on the machine.
func TestBrowserMetadataSymlinkIsNeverRead(t *testing.T) {
	root, outside := shortRoot(t), shortRoot(t)
	if err := os.MkdirAll(browserStateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	// A record that would otherwise be accepted, so only the link can be what
	// stops it.
	raw, err := json.Marshal(browserMetadata{
		Name: "browser", Socket: filepath.Join(browserStateDir(root), "browser.sock"), WindowID: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "borrowed.json")
	if err := os.WriteFile(secret, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, browserMetadataPath(root, "browser")); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}

	if paths, _ := browserCandidatePaths(root, ""); len(paths) != 0 {
		t.Errorf("discovery followed a metadata link to %v", paths)
	}
	if record, ok := managedBrowserPane(root, "w1"); ok {
		t.Errorf("close trusted a record read through a link: %+v", record)
	}
}

// A record this user wrote is still only trusted as far as it describes golder's
// own paths: the name becomes a file name close deletes, the socket decides what
// discovery probes, and the profile decides what a browser is pointed at.
func TestBrowserRecordMustNameGoldersOwnPaths(t *testing.T) {
	root := shortRoot(t)
	if err := os.MkdirAll(browserStateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(browserStateDir(root), "browser.sock")
	cases := []struct {
		name string
		meta browserMetadata
		ok   bool
	}{
		{name: "golders own record", meta: browserMetadata{Name: "browser", Socket: socket, Profile: browserProfileDir(root, "browser")}, ok: true},
		{name: "no profile", meta: browserMetadata{Name: "browser", Socket: socket}, ok: true},
		{name: "escaping name", meta: browserMetadata{Name: "../../id_rsa", Socket: socket}, ok: false},
		{name: "dot name", meta: browserMetadata{Name: ".", Socket: socket}, ok: false},
		{name: "empty name", meta: browserMetadata{Name: "", Socket: socket}, ok: false},
		{name: "foreign socket", meta: browserMetadata{Name: "browser", Socket: filepath.Join(root, "outside", "browser.sock")}, ok: false},
		{name: "relative socket", meta: browserMetadata{Name: "browser", Socket: "browser.sock"}, ok: false},
		{name: "empty socket", meta: browserMetadata{Name: "browser"}, ok: false},
		{name: "foreign profile", meta: browserMetadata{Name: "browser", Socket: socket, Profile: filepath.Join(root, "outside")}, ok: false},
		{name: "another browsers profile", meta: browserMetadata{Name: "browser", Socket: socket, Profile: browserProfileDir(root, "other")}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBrowserRecord(root, tc.meta)
			if tc.ok && err != nil {
				t.Fatalf("refused golder's own record: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("accepted %+v", tc.meta)
			}
		})
	}
}

// Close deletes the record whose name it used, so a name from a record a
// checkout wrote must never become a path outside the state directory.
func TestCloseNeverDeletesThroughARecordName(t *testing.T) {
	root := shortRoot(t)
	state := browserStateDir(root)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// "../evil" resolves, one level up, to this file: the first thing a deletion
	// driven by an unvalidated name would hit.
	victim := filepath.Join(filepath.Dir(state), "evil.browser.json")
	raw, err := json.Marshal(browserMetadata{
		Name: "../evil", Socket: filepath.Join(state, "browser.sock"), WindowID: "w9", BrowserID: "browser-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Discovery of a record: refused by the record check, so no pane is found.
	forgetBrowserPane(root, "w9")
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("close deleted %s: %v", victim, err)
	}
	// And with the name in this process's own bookkeeping, the deletion guard
	// stops it before it builds a path.
	rememberBrowserPane(browserPaneRecord{Root: root, Name: "../evil", WindowID: "w9", Socket: filepath.Join(state, "browser.sock")})
	t.Cleanup(func() { forgetBrowserPane(root, "w9") })
	forgetBrowserPane(root, "w9")
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("close deleted %s from its own registry: %v", victim, err)
	}
}

// A record readable by others is not a record golder wrote: it is 0600 when it
// is written, and a looser one is ignored rather than trusted.
func TestBrowserMetadataMustBeOwnerOnly(t *testing.T) {
	root := shortRoot(t)
	if err := os.MkdirAll(browserStateDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	path := browserMetadataPath(root, "browser")
	if err := writeBrowserMetadata(path, browserMetadata{
		Name: "browser", Socket: filepath.Join(browserStateDir(root), "browser.sock"),
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("a written record is mode %04o, want 0600", perm)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBrowserMetadata(path); err == nil {
		t.Fatal("read a record anybody on the machine can rewrite")
	}
}

// The runtime directory a socket moves to is per-user and private: under a
// shared temp directory the name carries the uid, and the directory is 0700.
func TestBrowserRuntimeDirIsPrivateAndPerUser(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	// Both runtime roots are stated rather than inherited, so this test never
	// creates a directory in the developer's real temp directory.
	tmp := tinyRoot(t)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_RUNTIME_DIR", "")
	deep := deepWorkspace(t)

	spec, err := browserPaneSpec(deep, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(spec.Socket)
	if dir == browserStateDir(deep) {
		t.Fatalf("socket %q stayed in the workspace", spec.Socket)
	}
	if got, want := filepath.Dir(dir), filepath.Join(tmp, privatepath.RuntimeName()); got != want {
		t.Errorf("runtime root = %q, want %q", got, want)
	}
	if !strings.HasPrefix(filepath.Base(dir), "browser-") {
		t.Errorf("runtime directory %q does not name the project", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("runtime directory %s is %04o, want 0700", dir, perm)
	}
	if len(spec.Socket) > unixSocketPathLimit {
		t.Errorf("socket %q is %d bytes, over the %d limit", spec.Socket, len(spec.Socket), unixSocketPathLimit)
	}
}

// In a world-writable temp directory another account can pre-create the runtime
// directory — or a link under this account's name. It is refused, the socket
// falls back to the other short private candidate, and the link's target is left
// exactly as it was.
func TestBrowserRuntimeDirRefusesAPreCreatedSymlink(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	deep := deepWorkspace(t)
	xdg := tinyRoot(t)
	tmp := tinyRoot(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	t.Setenv("TMPDIR", tmp)

	target := filepath.Join(xdg, "grabbed")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// The first candidate — the $XDG_RUNTIME_DIR one — is pre-created as a link.
	link := browserRuntimeCandidates(deep)[0]
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, xdg) {
		t.Fatalf("the first runtime candidate %q is not under $XDG_RUNTIME_DIR %q", link, xdg)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}

	socket, err := browserControlSocketPath(deep, "browser")
	if err != nil {
		t.Fatalf("a symlinked runtime directory stopped a socket that had another home: %v", err)
	}
	if filepath.Dir(socket) == link {
		t.Fatal("the socket was placed through a symlink")
	}
	if want := filepath.Join(tmp, privatepath.RuntimeName()); !strings.HasPrefix(filepath.Dir(socket), want) {
		t.Errorf("socket %q is not in the fallback runtime root %q", socket, want)
	}
	if len(socket) > unixSocketPathLimit {
		t.Errorf("socket %q is %d bytes, over the %d limit", socket, len(socket), unixSocketPathLimit)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the link's target gained %v, want nothing", entries)
	}
}
