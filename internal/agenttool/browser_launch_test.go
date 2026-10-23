package agenttool

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
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
