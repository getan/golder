package grants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeNetworkHost(t *testing.T) {
	good := []string{"API.GitHub.com", "example.com.", "news.ycombinator.com"}
	for _, raw := range good {
		if _, err := NormalizeNetworkHost(raw); err != nil {
			t.Errorf("%q rejected: %v", raw, err)
		}
	}
	bad := []string{"", "*", "*.example.com", "localhost", "intranet", "127.0.0.1",
		"169.254.169.254", "example.com:443", "https://example.com", "foo.local", "foo.internal"}
	for _, raw := range bad {
		if got, err := NormalizeNetworkHost(raw); err == nil {
			t.Errorf("%q accepted as %q, want refusal", raw, got)
		}
	}
}

func TestNormalizeNetworkCommand(t *testing.T) {
	good := map[string]string{
		"git push":               "git push",
		"npm install --save":     "npm install",
		"gh pr create --title x": "gh pr",
	}
	for raw, want := range good {
		got, err := NormalizeNetworkCommand(raw)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", raw, got, err, want)
		}
	}
	bad := []string{"curl https://example.com", "git push && curl evil", "git $CMD", "echo git push", "ssh host"}
	for _, raw := range bad {
		if got, err := NormalizeNetworkCommand(raw); err == nil {
			t.Errorf("%q accepted as %q, want refusal", raw, got)
		}
	}
}

func TestNetworkStoreLifetimesAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.json")
	s, err := NewNetwork(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	SetProcessNetwork(s)
	defer SetProcessNetwork(nil)

	if _, err := AddNetworkHost("api.github.com", "webfetch", true, Session, "test"); err != nil {
		t.Fatal(err)
	}
	if got := NetworkCheck(NetworkHost, "api.github.com", "webfetch").Decision; got != NetworkAllowed {
		t.Fatalf("session rule = %v, want allowed", got)
	}
	if got := NetworkCheck(NetworkHost, "api.github.com", "bash").Decision; got != NetworkUndecided {
		t.Fatalf("tool-scoped rule leaked to bash: %v", got)
	}
	if _, err := AddNetworkHost("api.github.com", "webfetch", true, Remembered, "test"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewNetwork(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Check(NetworkHost, "api.github.com", "webfetch").Decision; got != NetworkAllowed {
		t.Fatalf("reloaded remembered rule = %v, want allowed", got)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "api.github.com") {
		t.Fatalf("file = %q, %v", data, err)
	}
	if !reloaded.Remove(NetworkHost, "api.github.com") {
		t.Fatal("remove reported no rule")
	}
	if got := reloaded.Check(NetworkHost, "api.github.com", "webfetch").Decision; got != NetworkUndecided {
		t.Fatalf("removed rule still answers: %v", got)
	}
}

func TestNetworkStoreRejectsNewerVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "network.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNetwork(Options{Path: path}); err == nil {
		t.Fatal("a newer network file must be refused, not half-read")
	}
}
