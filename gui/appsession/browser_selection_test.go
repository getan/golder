package appsession

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/getan/golder/internal/browserctl"
)

type selectionTransport struct {
	mu    sync.Mutex
	info  browserctl.Info
	posts int
}

func (f *selectionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet || r.URL.Path != "/v1/info" {
		f.posts++
	}
	raw, err := json.Marshal(f.info)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

func selectionFixture() (*BrowserSurface, *selectionTransport) {
	f := &selectionTransport{info: browserctl.Info{
		APIVersion: "v1", Backend: "native-cdp", BrowserID: "browser", ActiveTabID: "a",
		Tabs: []browserctl.Tab{
			{ID: "a", TargetID: "ta", URL: "https://a.test/", Active: true},
			{ID: "b", TargetID: "tb", URL: "https://b.test/"},
		},
	}}
	surface := &BrowserSurface{client: browserctl.NewWithTransport("stub", f), target: browserctl.Target{BrowserID: "browser", TabID: "a", TargetID: "ta"}}
	return surface, f
}

func (f *selectionTransport) selectTab(id, target string, revision uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info.ActiveTabID = id
	for i := range f.info.Tabs {
		f.info.Tabs[i].Active = f.info.Tabs[i].ID == id
	}
	f.info.ExplicitSelection = &browserctl.ExplicitSelection{Revision: revision, TabID: id, TargetID: target}
}

func TestSurfaceFollowsOnlyExplicitPinnedSelection(t *testing.T) {
	surface, f := selectionFixture()
	f.selectTab("b", "tb", 1)
	pending, err := surface.PendingSelection(context.Background())
	if err != nil || pending == nil || pending.TabID != "b" || surface.target.TabID != "a" {
		t.Fatalf("pending = %+v, target=%+v, err=%v", pending, surface.target, err)
	}
	if err := surface.FollowSelection(context.Background(), *pending); err != nil {
		t.Fatal(err)
	}
	if surface.target.TabID != "b" || surface.target.TargetID != "tb" {
		t.Fatalf("selection = %+v", surface.target)
	}
	if pending, err := surface.PendingSelection(context.Background()); err != nil || pending != nil {
		t.Fatalf("replayed selection = %+v, %v", pending, err)
	}
	if f.posts != 0 {
		t.Fatal("following a view triggered a browser mutation")
	}
}

func TestManualSelectionOverridesEarlierHostEvent(t *testing.T) {
	surface, f := selectionFixture()
	f.selectTab("a", "ta", 4)
	if err := surface.ActivateTab(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if pending, err := surface.PendingSelection(context.Background()); err != nil || pending != nil || surface.target.TabID != "b" {
		t.Fatalf("old event overrode manual selection: %+v, %+v, %v", pending, surface.target, err)
	}
	f.selectTab("a", "ta", 5)
	pending, err := surface.PendingSelection(context.Background())
	if err != nil || pending == nil || pending.Revision != 5 {
		t.Fatalf("new explicit event = %+v, %v", pending, err)
	}
}

func TestStaleOrUnsafeExplicitSelectionDoesNotSwitch(t *testing.T) {
	surface, f := selectionFixture()
	f.selectTab("b", "tb", 1)
	pending, err := surface.PendingSelection(context.Background())
	if err != nil || pending == nil {
		t.Fatalf("missing event: %+v %v", pending, err)
	}
	f.selectTab("a", "ta", 2)
	if err := surface.FollowSelection(context.Background(), *pending); err != nil || surface.target.TabID != "a" {
		t.Fatalf("stale event switched tab: %+v, %v", surface.target, err)
	}
	f.mu.Lock()
	f.info.Tabs[1].URL = "chrome://settings/help"
	f.mu.Unlock()
	f.selectTab("b", "tb", 3)
	if pending, err := surface.PendingSelection(context.Background()); err != nil || pending != nil || surface.target.TabID != "a" {
		t.Fatalf("internal event changed the view: %+v, %+v, %v", pending, surface.target, err)
	}
}

func TestLegacyHostFollowsOnlyExactActiveTarget(t *testing.T) {
	surface, f := selectionFixture()
	f.mu.Lock()
	f.info.ActiveTabID = "b"
	f.mu.Unlock()
	wrong := BrowserSelection{BrowserID: "browser", TabID: "b", TargetID: "wrong"}
	if err := surface.FollowSelection(context.Background(), wrong); err != nil || surface.target.TabID != "a" {
		t.Fatalf("wrong target changed the view: %+v, %v", surface.target, err)
	}
	correct := BrowserSelection{BrowserID: "browser", TabID: "b", TargetID: "tb"}
	if err := surface.FollowSelection(context.Background(), correct); err != nil || surface.target.TabID != "b" {
		t.Fatalf("legacy selection = %+v, %v", surface.target, err)
	}
}
