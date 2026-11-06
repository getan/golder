//go:build darwin || linux

package mygoui

import (
	"context"
	"encoding/json"
	"image"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/browserctl"
)

type queuedBrowserUpdater struct{ updates chan func() }

func (u *queuedBrowserUpdater) Update(fn func()) { u.updates <- fn }

// Exercise the complete GUI event-to-surface path against a private in-process
// control socket, without a native desktop window or a real browser profile.
func TestBrowserSelectionEventChangesVisibleSurface(t *testing.T) {
	root, err := os.MkdirTemp(os.TempDir(), "gb-follow-ui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "browser.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	info := browserctl.Info{APIVersion: "v1", Backend: "native-cdp", BrowserID: "browser", ActiveTabID: "a", Tabs: []browserctl.Tab{
		{ID: "a", TargetID: "ta", URL: "https://a.test/", Active: true},
		{ID: "b", TargetID: "tb", URL: "https://b.test/"},
	}}
	posts := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/info" {
			posts++
			http.Error(w, "unexpected action", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	surface, err := appsession.OpenBrowserSurface(ctx, appsession.BrowserPreview{Socket: socket, Browser: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	defer surface.Close()
	updater := &queuedBrowserUpdater{updates: make(chan func(), 4)}
	state := &browserWindowState{win: updater, surface: surface, socket: browserSocketKey(socket), browserID: "browser", bitmap: ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 2, 2))), frameW: 2, frameH: 2}
	a := testApp()
	a.browserState, a.browserPanelOpen = state, true
	mu.Lock()
	info.ActiveTabID = "b"
	info.Tabs[0].Active, info.Tabs[1].Active = false, true
	info.ExplicitSelection = &browserctl.ExplicitSelection{Revision: 7, TabID: "b", TargetID: "tb"}
	mu.Unlock()
	item := selectedBrowserItem("select-b", socket, "browser", "b", "tb")
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	if !state.switching.Load() || state.bitmap != nil || state.frameW != 0 || state.frameH != 0 {
		t.Fatal("old frame remained interactive while switching")
	}
	select {
	case update := <-updater.updates:
		update()
	case <-ctx.Done():
		t.Fatal("GUI follow did not complete")
	}
	if _, rawURL, _ := surface.State(); rawURL != "https://b.test/" || state.switching.Load() || len(state.tabs) != 2 || !state.tabs[1].Active {
		t.Fatalf("visible surface = %q, switching=%v, tabs=%+v", rawURL, state.switching.Load(), state.tabs)
	}
	background := appsession.Item{ID: "observe-a", Tool: &appsession.ToolState{Name: "browser", Input: map[string]any{"action": "snapshot", "tab": "a"}}}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &background})
	if _, rawURL, _ := surface.State(); rawURL != "https://b.test/" || state.switching.Load() {
		t.Fatalf("background observation changed the visible surface: %q", rawURL)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 0 {
		t.Fatalf("GUI follow sent %d browser mutations", posts)
	}
}
