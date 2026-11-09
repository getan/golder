package appsession

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/browserctl"
)

func TestOldBrowserHostRejectsPointerInsteadOfEvaluatingScript(t *testing.T) {
	target := browserctl.Target{BrowserID: "b", TabID: "t", TargetID: "d"}
	surface := &BrowserSurface{target: target}
	err := surface.dispatchInput(context.Background(), surfaceInput{target: target, event: BrowserInput{Kind: BrowserPointerDown}})
	if err == nil || !strings.Contains(err.Error(), "does not support native pointer") {
		t.Fatalf("unsupported host: %v", err)
	}
}

type surfaceRoundTrip func(*http.Request) (*http.Response, error)

func (f surfaceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenBrowserSurfaceRejectsReplacedBrowser(t *testing.T) {
	previous := browserctlDial
	t.Cleanup(func() { browserctlDial = previous })
	browserctlDial = func(string) (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", surfaceRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"apiVersion":"v1","backend":"native-cdp","browserId":"replacement","tabs":[]}`))}, nil
		})), nil
	}
	if _, err := OpenBrowserSurface(context.Background(), BrowserPreview{Browser: "original"}); err == nil {
		t.Fatal("accepted a different browser at the discovered socket")
	}
}

func TestBrowserSurfaceSelectionUsesHostTab(t *testing.T) {
	var actions []string
	active := "a"
	client := browserctl.NewWithTransport("stub", surfaceRoundTrip(func(r *http.Request) (*http.Response, error) {
		var body string
		if r.URL.Path == "/v1/info" {
			body = `{"apiVersion":"v1","backend":"native-cdp","browserId":"b","activeTabId":"` + active + `","tabs":[{"id":"a","targetId":"ta","url":"https://a.test"},{"id":"b","targetId":"tb","url":"https://b.test"}]}`
		} else {
			var req struct {
				Action string            `json:"action"`
				Target browserctl.Target `json:"target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, req.Action)
			if req.Action == "tab.activate" {
				active = req.Target.TabID
			}
			if req.Target.TabID != "b" || req.Target.TargetID != "tb" {
				t.Fatalf("action targeted %+v", req.Target)
			}
			body = `{"ok":true,"result":{"mimeType":"image/png","data":"YWJj"},"meta":{"browserId":"b","tabId":"b","targetId":"tb"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}))
	surface := &BrowserSurface{client: client, target: browserctl.Target{BrowserID: "b", TabID: "a", TargetID: "ta"}}
	if err := surface.ActivateTab(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	tabs, err := surface.Tabs(context.Background())
	if err != nil || len(tabs) != 2 || tabs[0].Active || !tabs[1].Active {
		t.Fatalf("surface tabs = %+v, %v", tabs, err)
	}
	frame, err := surface.Frame(context.Background())
	if err != nil || string(frame) != "abc" || len(actions) != 2 || actions[0] != "tab.activate" || actions[1] != "screenshot" {
		t.Fatalf("frame = %q, actions = %v, err = %v", frame, actions, err)
	}
}

func TestBrowserSurfaceDiscardsOldTabResponsesAndQueuedInput(t *testing.T) {
	old := browserctl.Target{BrowserID: "b", TabID: "a", TargetID: "ta"}
	current := browserctl.Target{BrowserID: "b", TabID: "b", TargetID: "tb"}
	client := browserctl.NewWithTransport("stub", surfaceRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("stale input was sent to the browser")
		return nil, nil
	}))
	surface := &BrowserSurface{client: client, target: current, title: "current"}
	if surface.updateSelectedMeta(old, browserctl.Meta{BrowserID: "b", TabID: "a", TargetID: "ta", Title: "stale"}) {
		t.Fatal("accepted metadata from the old selection")
	}
	if err := surface.dispatchInput(context.Background(), surfaceInput{target: old, event: BrowserInput{Kind: BrowserText, Text: "stale"}}); err == nil {
		t.Fatal("stale input was silently discarded")
	}
	if surface.target != current || surface.title != "current" {
		t.Fatalf("selection was changed: %+v", surface)
	}
}
