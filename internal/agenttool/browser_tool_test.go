package agenttool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

// fakeBrowser serves the kitty-browse control API over a stub transport. The
// tool only speaks HTTP, so no real Unix socket is needed to test it.
type fakeBrowser struct {
	mu      sync.Mutex
	actions []map[string]any
	replies map[string]string
}

func (f *fakeBrowser) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/info":
		body = `{"apiVersion":"v1","browserId":"browser-1","activeTabId":"tab-2","backend":"native-cdp",
			"tabs":[
				{"id":"tab-1","targetId":"t1","url":"http://localhost:3000/","title":"App","index":1,"active":false},
				{"id":"tab-2","targetId":"t2","url":"http://localhost:3000/login","title":"Login","index":2,"active":true}
			],"capabilities":[]}`
	case r.Method == http.MethodGet && r.URL.Path == "/v1/screenshot":
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\nfake")),
		}, nil
	case r.Method == http.MethodPost && r.URL.Path == "/v1/actions":
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		f.mu.Lock()
		f.actions = append(f.actions, req)
		action, _ := req["action"].(string)
		result, ok := f.replies[action]
		f.mu.Unlock()
		if !ok {
			result = `{"ok":true}`
		}
		body = `{"apiVersion":"v1","id":"x","ok":true,"result":` + result +
			`,"meta":{"browserId":"browser-1","tabId":"tab-2","url":"http://localhost:3000/","title":"App","backend":"native-cdp"}}`
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func newFakeBrowserTool(t *testing.T, allowControl bool, root string) *BrowserTool {
	t.Helper()
	f := &fakeBrowser{replies: map[string]string{}}
	tool := NewBrowserTool(root, allowControl)
	tool.newClient = func() (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", f), nil
	}
	t.Cleanup(func() {})
	return tool
}

func fakeBrowserOf(t *testing.T, tool *BrowserTool) *fakeBrowser {
	t.Helper()
	c, err := tool.newClient()
	_ = c
	if err != nil {
		t.Fatal(err)
	}
	// The stub is reachable only through the closure; the tests that need it
	// build it themselves.
	return nil
}

func TestBrowserListReportsTabs(t *testing.T) {
	tool := newFakeBrowserTool(t, true, t.TempDir())
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list failed: %s", agentcore.ContentToText(res.Content))
	}
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{"tab-1", "tab-2", "browser-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("list = %s, want %s", text, want)
		}
	}
}

func TestBrowserSnapshotPinsTheActiveTab(t *testing.T) {
	f := &fakeBrowser{replies: map[string]string{}}
	tool := NewBrowserTool(t.TempDir(), true)
	tool.newClient = func() (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", f), nil
	}
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"snapshot","interactive":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("snapshot failed: %s", agentcore.ContentToText(res.Content))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.actions) != 1 || f.actions[0]["action"] != "snapshot" {
		t.Fatalf("actions = %v, want one snapshot", f.actions)
	}
	target, _ := f.actions[0]["target"].(map[string]any)
	if target["tabId"] != "tab-2" || target["browserId"] != "browser-1" || target["targetId"] != "t2" {
		t.Errorf("target = %v, want the active tab pinned", target)
	}
}

func TestBrowserClickCarriesTheRef(t *testing.T) {
	f := &fakeBrowser{replies: map[string]string{}}
	tool := NewBrowserTool(t.TempDir(), true)
	tool.newClient = func() (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", f), nil
	}
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"click","ref":"@e12"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("click failed: %s", agentcore.ContentToText(res.Content))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	params, _ := f.actions[0]["params"].(map[string]any)
	if params["ref"] != "@e12" {
		t.Errorf("params = %v, want the ref passed through", params)
	}
}

// A sub-agent gets the read schema and the same refusal at execution.
func TestBrowserSubAgentCannotClick(t *testing.T) {
	f := &fakeBrowser{replies: map[string]string{}}
	tool := NewBrowserTool(t.TempDir(), false)
	tool.newClient = func() (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", f), nil
	}
	if strings.Contains(string(tool.Schema()), `"click"`) {
		t.Error("the read schema must not advertise click")
	}
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"click","ref":"@e1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a sub-agent click must be refused")
	}
	if len(f.actions) != 0 {
		t.Errorf("actions = %v, want none", f.actions)
	}
}

func TestBrowserScreenshotWritesIntoWorkspace(t *testing.T) {
	root := t.TempDir()
	tool := newFakeBrowserTool(t, true, root)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"screenshot"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("screenshot failed: %s", agentcore.ContentToText(res.Content))
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(agentcore.ContentToText(res.Content)), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Path, filepath.Join(root, ".golder", "artifacts")) {
		t.Fatalf("path = %q, want under the workspace", out.Path)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Fatalf("screenshot was not written: %v", err)
	}
}

// A path another user can write is refused before any action is sent.
func TestBrowserRefusesANonSocket(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := browserctl.Dial(file); err == nil || !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("Dial(%q) = %v, want the socket refusal", file, err)
	}
}

// A stale ref is the one error with a specific remedy, and the message must
// say it rather than echo the code alone.
func TestBrowserStaleRefSaysResnapshot(t *testing.T) {
	res := browserError(&browserctl.Error{Code: "stale_ref", Message: "the element is gone"})
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "stale_ref") || !strings.Contains(text, "snapshot again") {
		t.Fatalf("error = %q, want the stale-ref remedy", text)
	}
}

// The tab actions are host-managed, and their names and parameters are not the
// tool's own: a wrong name fails with unknown_action and a missing id selects
// nothing.
func TestBrowserTabActionsMapToTheHostAPI(t *testing.T) {
	action, params, err := browserActionParams(browserArgs{Action: "tab_select", Tab: "t1"})
	if err != nil || action != "tab.activate" || params["tabId"] != "t1" {
		t.Errorf("tab_select = %q %v %v, want tab.activate with tabId", action, params, err)
	}
	action, params, err = browserActionParams(browserArgs{Action: "tab_close", Tab: "t2"})
	if err != nil || action != "tab.close" || params["tabId"] != "t2" {
		t.Errorf("tab_close = %q %v %v, want tab.close with tabId", action, params, err)
	}
	action, params, err = browserActionParams(browserArgs{Action: "tab_new", URL: "https://example.com"})
	if err != nil || action != "tab.new" || params["url"] != "https://example.com" {
		t.Errorf("tab_new = %q %v %v, want tab.new with the url", action, params, err)
	}
	for _, name := range []string{"tab_select", "tab_close"} {
		if _, _, err := browserActionParams(browserArgs{Action: name}); err == nil {
			t.Errorf("%s without a tab should fail before the round trip", name)
		}
	}
}
