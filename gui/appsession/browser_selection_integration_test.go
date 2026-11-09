//go:build darwin || linux

package appsession

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browserbinding"
	"github.com/getan/golder/internal/browserctl"
)

// Opt-in: the socket and renderer use a private throwaway profile and loopback
// fixtures. Host foreground, rather than transcript events or selection-revision
// support, is the source of truth for both Agent and operator surfaces.
func TestBrowserSurfaceFollowsExplicitSelectionIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if os.Getenv("GOLDER_TEST_BROWSER_FOLLOW") != "1" || chrome == "" {
		t.Skip("set GOLDER_TEST_BROWSER_FOLLOW=1 and KITTY_BROWSE_CHROME to run the browser integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "<!doctype html><title>%s</title><body>%s</body>", r.URL.Path, r.URL.Path)
	}))
	defer server.Close()
	root, err := os.MkdirTemp(os.TempDir(), "gbfollow-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "browser.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	host, err := browserhost.Start(ctx, browserhost.Options{URL: server.URL + "/a", Socket: socket, Chrome: chrome, Private: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close(); _ = host.Wait() }()
	client, err := browserctl.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	info, err := client.Info(ctx)
	if err != nil || len(info.Tabs) != 1 {
		t.Fatalf("initial info = %+v, %v", info, err)
	}
	a := info.Tabs[0]
	surface, err := OpenBrowserSurface(ctx, BrowserPreview{Socket: socket, Browser: info.BrowserID})
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	_, b, err := client.Action(ctx, "tab.new", map[string]any{"url": server.URL + "/b"}, browserctl.Target{BrowserID: info.BrowserID}, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Tabs(ctx); err != nil {
		t.Fatal(err)
	}
	selectAndFollow := func(tabID, targetID, wantURL string) {
		t.Helper()
		target := browserctl.Target{BrowserID: info.BrowserID, TabID: tabID, TargetID: targetID}
		_, meta, err := client.Action(ctx, "tab.activate", map[string]any{"tabId": tabID}, target, 15*time.Second)
		if err != nil || meta.TabID != tabID || meta.TargetID != targetID {
			t.Fatalf("activate = %+v, %v", meta, err)
		}
		if _, err := surface.Tabs(ctx); err != nil {
			t.Fatal(err)
		}
		if _, current, _ := surface.State(); current != wantURL {
			t.Fatalf("visible tab = %q, want %q", current, wantURL)
		}
	}
	selectAndFollow(b.TabID, b.TargetID, server.URL+"/b")
	tool := agenttool.NewBrowserTool(root, true)
	binding := agenttool.ConfigureBrowserSession([]agentcore.AgentTool{tool}, "foreground-integration")
	binding.Select(browserbinding.Endpoint{Socket: socket, BrowserID: info.BrowserID})
	prepared, err := tool.PrepareCall(ctx, []byte(`{"action":"snapshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := surface.ActivateTab(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(ctx, "already-prepared", prepared, nil)
	details, _ := result.Details.(map[string]any)
	if err != nil || result.IsError || details["url"] != server.URL+"/b" {
		t.Fatalf("manual switch redirected an already prepared Agent call: %+v %v", result, err)
	}
	prepared, err = tool.PrepareCall(ctx, []byte(`{"action":"snapshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err = tool.Execute(ctx, "next-call", prepared, nil)
	details, _ = result.Details.(map[string]any)
	if err != nil || result.IsError || details["url"] != server.URL+"/a" {
		t.Fatalf("next Agent call did not follow manual foreground: %+v %v", result, err)
	}
	selectAndFollow(a.ID, a.TargetID, server.URL+"/a")
	if _, _, err := client.Action(ctx, "screenshot", map[string]any{"full": false}, browserctl.Target{BrowserID: info.BrowserID, TabID: b.TabID, TargetID: b.TargetID}, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := surface.Tabs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, current, _ := surface.State(); current != server.URL+"/a" {
		t.Fatalf("background screenshot changed the foreground: %q", current)
	}
}
