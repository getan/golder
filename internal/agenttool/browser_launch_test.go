package agenttool

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := exec.LookPath("kitty-browse"); err != nil {
		t.Skip("kitty-browse is not installed")
	}
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
