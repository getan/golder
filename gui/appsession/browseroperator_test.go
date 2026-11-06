package appsession

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/getan/golder/internal/browserctl"
)

type fakeBrowserOperator struct{ events chan map[string]any }

func (f *fakeBrowserOperator) SendOperatorInputJSON(_ context.Context, raw []byte) error {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	f.events <- event
	return nil
}

func TestBrowserOperatorRegistrationDoesNotRemoveReplacement(t *testing.T) {
	first, second := &fakeBrowserOperator{}, &fakeBrowserOperator{}
	socket := t.TempDir() + "/browser.sock"
	removeFirst := RegisterBrowserOperator(socket, first)
	removeSecond := RegisterBrowserOperator(socket, second)
	defer removeSecond()
	removeFirst()
	if got := browserOperator(socket); got != second {
		t.Fatal("an older host's cleanup removed the replacement")
	}
	removeSecond()
	if got := browserOperator(socket); got != nil {
		t.Fatal("operator remained registered after its host exited")
	}
}

func TestBrowserOperatorHoverDragAndTabSwitchRelease(t *testing.T) {
	operator := &fakeBrowserOperator{events: make(chan map[string]any, 8)}
	target := browserctl.Target{BrowserID: "browser", TabID: "tab", TargetID: "target"}
	surface := &BrowserSurface{operator: operator, target: target}
	for _, tc := range []struct {
		input BrowserInput
		kind  string
	}{
		{BrowserInput{Kind: BrowserPointerMove, X: 1, Y: 2}, "pointer-hover"},
		{BrowserInput{Kind: BrowserPointerDown, X: 3, Y: 4}, "pointer-down"},
		{BrowserInput{Kind: BrowserPointerMove, X: 5, Y: 6}, "pointer-move"},
	} {
		if err := surface.dispatchInput(context.Background(), surfaceInput{target: target, event: tc.input}); err != nil {
			t.Fatal(err)
		}
		got := <-operator.events
		if got["kind"] != tc.kind || got["tabId"] != target.TabID {
			t.Fatalf("operator event = %+v, want %s on %s", got, tc.kind, target.TabID)
		}
	}
	surface.mu.Lock()
	surface.selectTabLocked("browser", browserctl.Tab{ID: "other", TargetID: "other-target"})
	surface.mu.Unlock()
	select {
	case got := <-operator.events:
		if got["kind"] != "pointer-up" || got["tabId"] != target.TabID || got["x"] != float64(5) || got["y"] != float64(6) {
			t.Fatalf("drag release = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tab switch did not release the pointer on its original target")
	}
}
