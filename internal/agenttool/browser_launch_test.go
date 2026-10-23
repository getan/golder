package agenttool

import (
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
func TestAdoptExistingBrowserTakesALiveSocket(t *testing.T) {
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

	if got := adoptExistingBrowser(root); got != sock {
		t.Fatalf("adopt = %q, want %q", got, sock)
	}
}

// A socket nobody answers on is left over from a dead browser. It has to go, or
// kitty-browse refuses to start on the same path.
func TestAdoptExistingBrowserRemovesAStaleSocket(t *testing.T) {
	root := shortRoot(t)
	sock := browserSocketPath(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	_ = os.Chmod(sock, 0o600)
	_ = ln.Close() // leaves the socket file behind, with nothing listening

	if got := adoptExistingBrowser(root); got != "" {
		t.Fatalf("adopt = %q, want no adoption", got)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket still present: %v", err)
	}
}

// A regular file at the socket path is not a browser.
func TestAdoptExistingBrowserIgnoresANonSocket(t *testing.T) {
	root := shortRoot(t)
	sock := browserSocketPath(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := adoptExistingBrowser(root); got != "" {
		t.Fatalf("adopt = %q, want no adoption", got)
	}
}
