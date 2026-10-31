package agenttool

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func browserSocketPath(root string) string {
	return filepath.Join(root, ".golder", "browser", "browser.sock")
}

// shortRoot keeps the socket path under the macOS sockaddr_un limit (104 bytes).
// t.TempDir embeds the test name, which is long enough to hit it.
func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "kbt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// selfExecutable is the binary a real main would pass: this test binary stands
// in for golder itself. Nothing else may enable the built-in pane, which is the
// point of the seam.
func selfExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// withEmbeddedBrowser sets the built-in browser executable for one test and
// restores whatever was configured before, so state cannot leak between tests.
func withEmbeddedBrowser(t *testing.T, executable string) {
	t.Helper()
	embeddedBrowserMu.RLock()
	prevExe, prevErr := embeddedBrowserExecutable, embeddedBrowserErr
	embeddedBrowserMu.RUnlock()
	t.Cleanup(func() {
		embeddedBrowserMu.Lock()
		embeddedBrowserExecutable, embeddedBrowserErr = prevExe, prevErr
		embeddedBrowserMu.Unlock()
	})
	if err := ConfigureEmbeddedBrowser(executable); err != nil {
		t.Fatalf("configure embedded browser %q: %v", executable, err)
	}
}

// A browser started by an earlier golder process is still reachable: the socket
// is live, so a new session adopts it instead of starting a second pane (which
// kitty-browse would refuse, since the path exists).
func TestDiscoverExistingBrowserTakesALiveSocket(t *testing.T) {
	root := shortRoot(t)
	sock := browserSocketPath(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"apiVersion":"v1","browserId":"b1","backend":"native-cdp","tabs":[]}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	d := discoverBrowsers(context.Background(), root, "")
	if len(d.Instances) != 1 || d.Instances[0].Socket != canonicalBrowserSocket(sock) {
		t.Fatalf("discovery = %+v, want %q", d, sock)
	}
}

// An unresponsive socket must never be removed by discovery.
func TestDiscoveryPreservesAStaleSocket(t *testing.T) {
	root := shortRoot(t)
	sock := browserSocketPath(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	_ = os.Chmod(sock, 0o600)
	ln.SetUnlinkOnClose(false)
	_ = ln.Close() // leaves the socket file behind, with nothing listening

	d := discoverBrowsers(context.Background(), root, "")
	if len(d.Instances) != 0 || len(d.Failures) != 1 {
		t.Fatalf("discovery = %+v, want an unavailable endpoint", d)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("discovery removed an existing socket: %v", err)
	}
}

// A regular file at the socket path is not a browser.
func TestDiscoveryIgnoresANonSocket(t *testing.T) {
	root := shortRoot(t)
	sock := browserSocketPath(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := discoverBrowsers(context.Background(), root, ""); len(d.Instances) != 0 {
		t.Fatalf("discovery = %+v, want no instance", d)
	}
}

func TestPruneBrowserProfilesKeepsCurrentAndFresh(t *testing.T) {
	dir := t.TempDir()
	makeProfile := func(name string, age time.Duration) string {
		path := filepath.Join(dir, name+".profile")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}
	keep := makeProfile("keep", 30*24*time.Hour)
	stale := makeProfile("stale", 30*24*time.Hour)
	fresh := makeProfile("fresh", time.Hour)

	pruneBrowserProfiles(dir, "keep", time.Now())

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("current profile removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale profile survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh profile removed: %v", err)
	}
}

func TestStaleBrowserSocketDetectsClosedListener(t *testing.T) {
	root := shortRoot(t)
	sock := filepath.Join(root, "stale.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	ln.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = os.Remove(sock) })

	if staleBrowserSocket(sock) {
		t.Fatal("live socket reported stale")
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if !staleBrowserSocket(sock) {
		t.Fatal("closed socket was not detected as stale")
	}
}

func TestBrowserPaneSpecReusesCanonicalAfterStaleSocket(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	dir := filepath.Join(root, ".golder", "browser")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "browser.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	spec, err := browserPaneSpec(root, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Socket != sock {
		t.Fatalf("socket = %q, want canonical %q", spec.Socket, sock)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket survived: %v", err)
	}
}

// The pane command is golder's own binary with its browse subcommand. PATH is
// empty, so nothing here can be a kitty-browse lookup in disguise.
func TestEmbeddedBrowserPaneSpecUsesOwnBrowseArgvWithEmptyPath(t *testing.T) {
	self := selfExecutable(t)
	withEmbeddedBrowser(t, self)
	t.Setenv("PATH", "")
	root := shortRoot(t)

	spec, err := browserPaneSpec(root, "browser", "https://example.com/app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		self, "browse",
		"-profile-dir", browserProfileDir(root, "browser"),
		"-control-socket", filepath.Join(browserStateDir(root), "browser.sock"),
		"https://example.com/app",
	}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv = %q, want %q", spec.Argv, want)
	}
	if !spec.Embedded || spec.Socket != browserSocketPath(root) {
		t.Fatalf("spec = %+v", spec)
	}
	if !filepath.IsAbs(spec.Argv[0]) {
		t.Fatalf("argv0 %q is not absolute", spec.Argv[0])
	}
}

func TestControlledBrowserPaneCarriesExplicitProxyFlag(t *testing.T) {
	self := selfExecutable(t)
	withEmbeddedBrowser(t, self)
	root := shortRoot(t)
	guard := NewControlGuard(root)
	guard.BrowserProxy = func() (string, error) { return "http://127.0.0.1:7897", nil }

	spec, err := controlledBrowserSpec(guard, root, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{"-proxy-server", "http://127.0.0.1:7897", "https://example.com"}
	if len(spec.Argv) < len(wantTail) || !slices.Equal(spec.Argv[len(spec.Argv)-len(wantTail):], wantTail) {
		t.Fatalf("browser argv = %q, want tail %q", spec.Argv, wantTail)
	}
}

// Registration asks "can a browser be started here". With the built-in pane
// configured that is true even when kitty-browse is nowhere on PATH.
func TestBrowserToolRegistrationWorksWithoutKittyBrowse(t *testing.T) {
	t.Setenv("PATH", "")
	withEmbeddedBrowser(t, "")
	if BrowserLaunchAvailable() || KittyBrowseAvailable() {
		t.Fatal("claims a browser can be started with no launcher configured")
	}
	withEmbeddedBrowser(t, selfExecutable(t))
	if !BrowserLaunchAvailable() || !KittyBrowseAvailable() {
		t.Fatal("built-in browse did not make the browser tool registrable")
	}
}

// Without either launcher the spec fails with advice, and never invents an argv.
func TestBrowserPaneSpecWithoutAnyLauncherIsExplicit(t *testing.T) {
	t.Setenv("PATH", "")
	withEmbeddedBrowser(t, "")
	spec, err := browserPaneSpec(shortRoot(t), "browser", "https://example.com")
	if err == nil || !strings.Contains(err.Error(), "kitty-browse") {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
	if len(spec.Argv) != 0 {
		t.Fatalf("invented an argv: %q", spec.Argv)
	}
}

func TestConfigureEmbeddedBrowserRejectsWhatCannotWork(t *testing.T) {
	withEmbeddedBrowser(t, "")
	notExecutable := filepath.Join(t.TempDir(), "golder")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, executable := range map[string]string{
		"directory":      t.TempDir(),
		"missing":        filepath.Join(t.TempDir(), "golder"),
		"not executable": notExecutable,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ConfigureEmbeddedBrowser(executable); err == nil {
				t.Fatalf("accepted %q as a browser executable", executable)
			}
			if EmbeddedBrowserConfigured() {
				t.Fatal("a refused configuration still counts as configured")
			}
		})
	}
	// The refusal stays visible where it matters instead of turning into a
	// silent fallback that hides a broken installer.
	if err := ConfigureEmbeddedBrowser(notExecutable); err == nil {
		t.Fatal("accepted a non-executable file")
	}
	t.Setenv("PATH", "")
	if _, err := browserPaneSpec(shortRoot(t), "browser", "https://example.com"); err == nil ||
		!strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("spec did not report the refused configuration: %v", err)
	}
}

// An empty executable disables the built-in pane again, which is the documented
// way for a caller (or a test) to restore the previous state.
func TestConfigureEmbeddedBrowserResetDisables(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	if !EmbeddedBrowserConfigured() {
		t.Fatal("configuration did not take effect")
	}
	if err := ConfigureEmbeddedBrowser(""); err != nil || EmbeddedBrowserConfigured() {
		t.Fatalf("reset err=%v configured=%v", err, EmbeddedBrowserConfigured())
	}
}

// A profile holds live sessions, so the directory and the profile are created
// owner-only even when an older version left them readable.
func TestBrowserPaneSpecKeepsBrowserStateOwnerOnly(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	dir := browserStateDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := browserPaneSpec(root, "browser", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, browserProfileDir(root, "browser")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s is %04o, want 0700", path, perm)
		}
	}
}

// A live endpoint keeps its path, so the new pane gets a suffixed one — still
// inside the Unix limit.
func TestBrowserPaneSpecGivesALiveEndpointItsOwnSocket(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	live := newControlFake(t, browserSocketPath(root), "browser-1", "quit")
	spec, err := browserPaneSpec(root, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Socket == live.path {
		t.Fatal("reused a live control socket")
	}
	if !strings.HasPrefix(spec.Socket, filepath.Join(browserStateDir(root), "browser-")) || !strings.HasSuffix(spec.Socket, ".sock") {
		t.Fatalf("socket = %q", spec.Socket)
	}
	if len(spec.Socket) > unixSocketPathLimit {
		t.Fatalf("socket %q is %d bytes, over the %d limit", spec.Socket, len(spec.Socket), unixSocketPathLimit)
	}
}

// A workspace near the Unix limit must not fail at bind time in the pane. The
// socket moves to the project's private runtime directory, the workspace keeps
// a record of it, and discovery finds it from that record.
func TestBrowserControlSocketMovesToRuntimeDirWhenWorkspaceIsDeep(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	deep := filepath.Join(root, strings.Repeat("d", 70), "project")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	spec, err := browserPaneSpec(deep, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Dir(spec.Socket)
	if runtime == browserStateDir(deep) {
		t.Fatalf("socket %q stayed in the workspace", spec.Socket)
	}
	if len(spec.Socket) > unixSocketPathLimit {
		t.Fatalf("socket %q is %d bytes", spec.Socket, len(spec.Socket))
	}
	if !validBrowserSocketLocation(deep, spec.Socket) {
		t.Fatalf("runtime socket %q is not recognized for %q", spec.Socket, deep)
	}
	info, err := os.Stat(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("runtime directory %s is %04o, want 0700", runtime, perm)
	}

	// The next session only has .golder/browser to read.
	newControlFake(t, spec.Socket, "browser-1", "quit")
	if err := writeBrowserMetadata(browserMetadataPath(deep, "browser"), browserMetadata{
		Name: "browser", Socket: spec.Socket, WindowID: "w2", BrowserID: "browser-1", Embedded: true,
	}); err != nil {
		t.Fatal(err)
	}
	d := discoverBrowsers(context.Background(), deep, "")
	if len(d.Instances) != 1 || d.Instances[0].Socket != canonicalBrowserSocket(spec.Socket) {
		t.Fatalf("discovery = %+v, want the runtime socket %q", d, spec.Socket)
	}
	if record, ok := managedBrowserPane(deep, "w2"); !ok || record.Socket != spec.Socket || record.BrowserID != "browser-1" {
		t.Fatalf("pane record = %+v ok=%v", record, ok)
	}
}

// When even the runtime directory cannot fit the path, the error names the
// limit and what to shorten — never a silent bind failure in the pane.
func TestBrowserControlSocketPathReportsTheUnixLimit(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	root := shortRoot(t)
	deep := filepath.Join(root, strings.Repeat("d", 70), "project")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", filepath.Join(root, strings.Repeat("t", 90)))
	_, err := browserControlSocketPath(deep, "browser")
	if err == nil || !strings.Contains(err.Error(), "Unix socket path") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "shorter path") {
		t.Fatalf("error does not say what to do: %v", err)
	}
	if _, err := browserPaneSpec(deep, "browser", "https://example.com"); err == nil {
		t.Fatal("pane spec accepted an unbindable socket path")
	}
}

func TestBrowserControlSocketPathRejectsUnsafeNames(t *testing.T) {
	root := shortRoot(t)
	for _, name := range []string{"", ".", "..", "../escape", "a/b", "a\\b"} {
		if _, err := browserControlSocketPath(root, name); err == nil {
			t.Fatalf("accepted browser name %q", name)
		}
	}
}

// A metadata file is repository content: it may point at a socket golder could
// have created for this workspace, and nowhere else.
func TestBrowserMetadataCannotRedirectDiscovery(t *testing.T) {
	root, outside := shortRoot(t), shortRoot(t)
	foreign := newControlFake(t, filepath.Join(outside, "browser.sock"), "browser-1", "quit")
	if err := writeBrowserMetadata(browserMetadataPath(root, "browser"), browserMetadata{
		Name: "browser", Socket: foreign.path, WindowID: "w2", BrowserID: "browser-1",
	}); err != nil {
		t.Fatal(err)
	}
	if paths, _ := browserCandidatePaths(root, ""); len(paths) != 0 {
		t.Fatalf("metadata redirected discovery to %v", paths)
	}
	if record, ok := managedBrowserPane(root, "w2"); ok {
		t.Fatalf("metadata redirect was trusted for close: %+v", record)
	}
}

// Adoption is what turns a new pane into an identity: the wait answers the
// caller's context instead of sleeping out a fixed budget, and success records
// the exact pane id, socket and browserId that a later close requires.
func TestAdoptBrowserPaneBoundsTheWaitAndRecordsIdentity(t *testing.T) {
	withEmbeddedBrowser(t, selfExecutable(t))
	withBrowserSocket(t, "")
	root := shortRoot(t)
	spec, err := browserPaneSpec(root, "browser", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := adoptBrowserPane(ctx, root, spec, "w2", 5*time.Second)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context deadline", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("wait ignored the caller's context: %s", elapsed)
		}
	}

	newControlFake(t, spec.Socket, "browser-1", "quit")
	record, err := adoptBrowserPane(context.Background(), root, spec, "w2", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forgetBrowserPane(root, "w2") })
	if record.WindowID != "w2" || record.BrowserID != "browser-1" || record.Socket != spec.Socket || !record.Embedded {
		t.Fatalf("record = %+v", record)
	}
	if got := browserSocket(); got != spec.Socket {
		t.Fatalf("session socket = %q, want %q", got, spec.Socket)
	}
	meta, err := readBrowserMetadata(browserMetadataPath(root, "browser"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.WindowID != "w2" || meta.BrowserID != "browser-1" || meta.Socket != spec.Socket || !meta.Embedded {
		t.Fatalf("metadata = %+v", meta)
	}

	// A later process has only .golder/browser to go on.
	browserPaneRegistry.mu.Lock()
	delete(browserPaneRegistry.byWindow, "w2")
	browserPaneRegistry.mu.Unlock()
	later, ok := managedBrowserPane(root, "w2")
	if !ok || later.Socket != spec.Socket || later.BrowserID != "browser-1" || !later.Embedded {
		t.Fatalf("later-session lookup = %+v ok=%v", later, ok)
	}

	// Closing that pane removes the record, so discovery stops probing it.
	forgetBrowserPane(root, "w2")
	if _, ok := managedBrowserPane(root, "w2"); ok {
		t.Fatal("closed pane still has a record")
	}
	if _, err := os.Stat(browserMetadataPath(root, "browser")); !os.IsNotExist(err) {
		t.Fatalf("metadata survived the close: %v", err)
	}
}
