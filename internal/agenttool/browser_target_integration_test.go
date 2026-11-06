//go:build darwin || linux

package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

// Opt-in: exercises Golder against the exact kitty-browse version in go.mod,
// using only a temporary profile and a local fixture rather than a saved login.
func TestBrowserScreenshotPinsBackgroundTabIntegration(t *testing.T) {
	chrome := os.Getenv("KITTY_BROWSE_CHROME")
	if os.Getenv("GOLDER_TEST_BROWSER_TARGET") != "1" || chrome == "" {
		t.Skip("set GOLDER_TEST_BROWSER_TARGET=1 and KITTY_BROWSE_CHROME to run the browser integration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		color := "rgb(220,20,20)"
		if r.URL.Path == "/blue" {
			color = "rgb(20,20,220)"
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><style>html,body{margin:0;background:%s;width:100%%;height:100%%}</style><body></body>`, color)
	}))
	defer server.Close()
	root := shortRoot(t)
	socket := filepath.Join(root, "browser.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	host, err := browserhost.Start(ctx, browserhost.Options{
		URL: server.URL + "/red", Socket: socket, Chrome: chrome, Private: true,
		ViewportWidth: 240, ViewportHeight: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	client, err := browserctl.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	info, err := client.Info(ctx)
	if err != nil || len(info.Tabs) != 1 {
		t.Fatalf("initial browser info = %+v, %v", info, err)
	}
	red := info.Tabs[0]
	if _, _, err := client.Action(ctx, "tab.new", map[string]any{"url": server.URL + "/blue"}, browserctl.Target{BrowserID: info.BrowserID}, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	info, err = client.Info(ctx)
	if err != nil || info.ActiveTabID == red.ID {
		t.Fatalf("blue tab did not become active: %+v, %v", info, err)
	}
	tool := NewBrowserTool(root, true)
	tool.boundSocket, tool.boundBrowser = socket, info.BrowserID
	tool.lastTab, tool.lastTargetID = red.ID, red.TargetID
	prepared, err := tool.PrepareCall(ctx, json.RawMessage(`{"action":"screenshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(ctx, "screenshot", prepared, nil)
	if err != nil || result.IsError {
		t.Fatalf("targeted screenshot: %s (%v)", agentcore.ContentToText(result.Content), err)
	}
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("screenshot details = %#v", result.Details)
	}
	path, _ := details["path"].(string)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := img.At(120, 90).RGBA()
	if r <= b {
		t.Fatalf("screenshot captured blue active tab, not red pinned tab: red=%d blue=%d", r, b)
	}
}
