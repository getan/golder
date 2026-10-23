package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

func serveDiscoveredBrowser(t *testing.T, path, id string) *atomic.Int32 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	posts := &atomic.Int32{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/info" {
			_ = json.NewEncoder(w).Encode(browserctl.Info{APIVersion: "v1", Backend: "native-cdp", BrowserID: id, ActiveTabID: "tab-1", Tabs: []browserctl.Tab{{ID: "tab-1", TargetID: "target-1", URL: "https://example.com/", Active: true}}})
			return
		}
		posts.Add(1)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"instance":%q},"meta":{"browserId":%q,"tabId":"tab-1"}}`, id, id)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return posts
}

func runBrowserCall(t *testing.T, tool *BrowserTool, raw string) agentcore.AgentToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), "test", json.RawMessage(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBrowserDiscoveryStaleSelectionAndExplicitAttach(t *testing.T) {
	root := shortRoot(t)
	old := browserSocketPath(root)
	withBrowserSocket(t, old) // selected endpoint is absent, but two panes are live
	a := serveDiscoveredBrowser(t, filepath.Join(root, ".golder", "first.sock"), "first")
	secondPath := filepath.Join(root, ".golder", "browser", "second.sock")
	b := serveDiscoveredBrowser(t, secondPath, "second")
	tool := NewBrowserTool(root, true)
	tool.lastTab = "tab-from-dead-browser"
	listed := runBrowserCall(t, tool, `{"action":"list"}`)
	text := agentcore.ContentToText(listed.Content)
	if listed.IsError || !strings.Contains(text, `"first"`) || !strings.Contains(text, `"second"`) {
		t.Fatalf("list failed to find both browsers: %s", text)
	}
	if tool.socketPath() != old {
		t.Fatal("read-only list switched the browser")
	}
	if res := runBrowserCall(t, tool, `{"action":"click","ref":"@e1"}`); !res.IsError {
		t.Fatal("a stale connection must not silently reroute a click")
	}
	if a.Load() != 0 || b.Load() != 0 {
		t.Fatal("failed click was replayed on a live browser")
	}
	if res := runBrowserCall(t, tool, `{"action":"open"}`); !res.IsError {
		t.Fatal("open must ask for an attachment when multiple candidates exist")
	}
	if tool.socketPath() != old {
		t.Fatal("ambiguous open changed the selection")
	}
	res := runBrowserCall(t, tool, `{"action":"attach","browser":"second"}`)
	if res.IsError || canonicalBrowserSocket(tool.socketPath()) != canonicalBrowserSocket(secondPath) || tool.lastTab != "" {
		t.Fatalf("attach failed or retained old tab: %+v", res)
	}
	res = runBrowserCall(t, tool, `{"action":"get","kind":"title"}`)
	if res.IsError || a.Load() != 0 || b.Load() != 1 {
		t.Fatalf("action did not reach exactly the attached instance: %+v", res)
	}
}

func TestBrowserAttachOverridesEnvAndRejectsUnknownID(t *testing.T) {
	root := shortRoot(t)
	withBrowserSocket(t, "")
	t.Setenv("KITTY_BROWSE_SOCKET", filepath.Join(root, "missing.sock"))
	path := filepath.Join(root, ".golder", "live.sock")
	serveDiscoveredBrowser(t, path, "live")
	tool := NewBrowserTool(root, true)
	if res := runBrowserCall(t, tool, `{"action":"attach","browser":"unknown"}`); !res.IsError {
		t.Fatal("accepted an arbitrary id")
	}
	if res := runBrowserCall(t, tool, `{"action":"attach","browser":"live"}`); res.IsError {
		t.Fatal(agentcore.ContentToText(res.Content))
	}
	if canonicalBrowserSocket(tool.socketPath()) != canonicalBrowserSocket(path) {
		t.Fatal("environment prevented explicit attachment")
	}
	child := NewBrowserTool(root, false)
	if strings.Contains(string(child.Schema()), `"attach"`) {
		t.Fatal("child advertises attachment")
	}
	if res := runBrowserCall(t, child, `{"action":"attach","browser":"live"}`); !res.IsError {
		t.Fatal("child changed binding")
	}
}

func TestBrowserRefusesReplacementInstanceAtBoundPath(t *testing.T) {
	tool := newFakeBrowserTool(t, true, t.TempDir())
	tool.bind(browserInstance{Socket: "stub", Info: browserctl.Info{BrowserID: "previous-browser"}})
	res := runBrowserCall(t, tool, `{"action":"click","ref":"@e1"}`)
	if !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "attach explicitly") {
		t.Fatal("reused socket changed browser identity silently")
	}
}

func TestBrowserDiscoveryRejectsUnsafeAndUnrelatedEndpoints(t *testing.T) {
	root := shortRoot(t)
	path := filepath.Join(root, ".golder", "unsafe.sock")
	serveDiscoveredBrowser(t, path, "unsafe")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	d := discoverBrowsers(context.Background(), root, "")
	if len(d.Instances) != 0 || len(d.Failures) != 1 {
		t.Fatalf("unsafe socket accepted: %+v", d)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("discovery removed unsafe endpoint")
	}
}

func TestBrowserDiscoveryDoesNotFollowWorkspaceSymlinks(t *testing.T) {
	root, outside := shortRoot(t), shortRoot(t)
	serveDiscoveredBrowser(t, filepath.Join(outside, "live.sock"), "outside")
	if err := os.Symlink(outside, filepath.Join(root, ".golder")); err != nil {
		t.Fatal(err)
	}
	if paths, _ := browserCandidatePaths(root, ""); len(paths) != 0 {
		t.Fatalf("escaped workspace: %v", paths)
	}
}

func TestBrowserDiscoveryTimeoutDoesNotUnlinkSocket(t *testing.T) {
	root := shortRoot(t)
	path := filepath.Join(root, ".golder", "busy.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	_ = os.Chmod(path, 0o600)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	d := discoverBrowsers(ctx, root, "")
	if len(d.Instances) != 0 || time.Since(start) > time.Second {
		t.Fatalf("probe was not bounded: %+v", d)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("timed-out probe deleted a live socket")
	}
}

// Explicit opt-in only: inspect existing user panes, attach a chosen one and
// issue a read-only title query. Never start/close/navigate a real browser here.
func TestBrowserLiveWorkspaceAttach(t *testing.T) {
	root, path := os.Getenv("GOLDER_BROWSER_TEST_ROOT"), os.Getenv("GOLDER_BROWSER_TEST_SOCKET")
	if root == "" || path == "" {
		t.Skip("set GOLDER_BROWSER_TEST_ROOT and GOLDER_BROWSER_TEST_SOCKET for the live read-only check")
	}
	withBrowserSocket(t, filepath.Join(root, ".golder", "browser", "browser.sock"))
	tool := NewBrowserTool(root, true)
	d := tool.discovery(context.Background())
	t.Logf("discovered %d live browsers, %d unavailable endpoints", len(d.Instances), len(d.Failures))
	var chosen string
	for _, instance := range d.Instances {
		t.Logf("browser=%s selected=%v socket=%s", instance.Info.BrowserID, instance.Selected, instance.Socket)
		if instance.Socket == canonicalBrowserSocket(path) {
			chosen = instance.Info.BrowserID
		}
	}
	if chosen == "" {
		t.Fatal("requested test browser was not discovered")
	}
	args, _ := json.Marshal(map[string]string{"action": "attach", "browser": chosen})
	res := runBrowserCall(t, tool, string(args))
	if res.IsError {
		t.Fatal(agentcore.ContentToText(res.Content))
	}
	res = runBrowserCall(t, tool, `{"action":"get","kind":"title"}`)
	if res.IsError {
		t.Fatal(agentcore.ContentToText(res.Content))
	}
	t.Log(agentcore.ContentToText(res.Content))
}
