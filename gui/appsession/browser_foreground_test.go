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

type foregroundTransport struct {
	mu            sync.Mutex
	info          browserctl.Info
	actions       []string
	duringCapture func()
}

func (f *foregroundTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var raw []byte
	if r.URL.Path == "/v1/info" {
		raw, _ = json.Marshal(f.info)
	} else {
		var request struct {
			Action string            `json:"action"`
			Target browserctl.Target `json:"target"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.actions = append(f.actions, request.Action)
		target := request.Target
		if request.Action == "tab.activate" {
			f.info.ActiveTabID = target.TabID
		}
		var tab browserctl.Tab
		for _, candidate := range f.info.Tabs {
			if candidate.ID == target.TabID {
				tab = candidate
			}
		}
		if request.Action == "screenshot" && f.duringCapture != nil {
			f.duringCapture()
		}
		raw, _ = json.Marshal(map[string]any{"ok": true, "result": map[string]string{"mimeType": "image/png", "data": "YWJj"}, "meta": browserctl.Meta{BrowserID: f.info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID, URL: tab.URL, Title: tab.Title}})
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

func foregroundFixture() (*BrowserSurface, *foregroundTransport) {
	f := &foregroundTransport{info: browserctl.Info{APIVersion: "v1", Backend: "native-cdp", BrowserID: "browser", ActiveTabID: "a", Tabs: []browserctl.Tab{
		{ID: "a", TargetID: "ta", Title: "A", URL: "https://a.test/"},
		{ID: "b", TargetID: "tb", Title: "B", URL: "https://b.test/"},
	}}}
	s := &BrowserSurface{client: browserctl.NewWithTransport("stub", f), target: browserctl.Target{BrowserID: "browser", TabID: "a", TargetID: "ta"}, input: newBrowserInputQueue(8)}
	return s, f
}

func TestManualSelectionUpdatesHostForeground(t *testing.T) {
	s, f := foregroundFixture()
	if err := s.ActivateTab(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if f.info.ActiveTabID != "b" || s.target.TabID != "b" || len(f.actions) != 1 || f.actions[0] != "tab.activate" {
		t.Fatalf("manual selection diverged: target=%+v host=%+v actions=%v", s.target, f.info, f.actions)
	}
}

func TestSurfaceProjectsHostWithoutTranscriptOrSelectionRevision(t *testing.T) {
	s, f := foregroundFixture()
	f.info.ActiveTabID = "b" // either Agent or TUI operator changed the host
	tabs, err := s.Tabs(context.Background())
	if err != nil || s.target.TabID != "b" || !tabs[1].Active || len(f.actions) != 0 {
		t.Fatalf("projection = %+v, %v, actions=%v", tabs, err, f.actions)
	}
	frame, err := s.Capture(context.Background())
	if err != nil || frame.Target.TabID != "b" || frame.Title != "B" || frame.URL != "https://b.test/" || string(frame.PNG) != "abc" {
		t.Fatalf("frame = %+v, %v", frame, err)
	}
	if s.InputTarget(browserctl.Target{BrowserID: "browser", TabID: "a", TargetID: "ta"}, BrowserInput{Kind: BrowserText, Text: "stale"}) {
		t.Fatal("input for the previous frame was accepted")
	}
}

func TestCaptureDiscardsFrameAfterHostSwitch(t *testing.T) {
	s, f := foregroundFixture()
	f.duringCapture = func() { f.info.ActiveTabID = "b" }
	if frame, err := s.Capture(context.Background()); err == nil || len(frame.PNG) != 0 {
		t.Fatalf("old pixels escaped after host switched: %+v %v", frame, err)
	}
}

func TestMissingOrUnsafeForegroundDoesNotFallBack(t *testing.T) {
	for _, id := range []string{"closed", "internal"} {
		t.Run(id, func(t *testing.T) {
			s, f := foregroundFixture()
			f.info.Tabs = append(f.info.Tabs, browserctl.Tab{ID: "internal", TargetID: "ti", URL: "chrome://settings/"})
			f.info.ActiveTabID = id
			if _, err := s.Tabs(context.Background()); err == nil || s.target.TabID != "" {
				t.Fatalf("unsafe foreground fell back: %+v %v", s.target, err)
			}
		})
	}
}
