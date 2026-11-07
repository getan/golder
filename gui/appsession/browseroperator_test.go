package appsession

import (
	"context"
	"testing"
	"time"

	"github.com/getan/golder/internal/browserctl"
)

type fakeBrowserOperator struct{ events chan BrowserOperatorInput }

func (f *fakeBrowserOperator) SendOperatorInput(_ context.Context, event BrowserOperatorInput) error {
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
	operator := &fakeBrowserOperator{events: make(chan BrowserOperatorInput, 8)}
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
		if got.Kind != tc.kind || got.TabID != target.TabID {
			t.Fatalf("operator event = %+v, want %s on %s", got, tc.kind, target.TabID)
		}
	}
	surface.mu.Lock()
	surface.selectTabLocked("browser", browserctl.Tab{ID: "other", TargetID: "other-target"})
	surface.mu.Unlock()
	select {
	case got := <-operator.events:
		if got.Kind != "pointer-up" || got.TabID != target.TabID || got.X != 5 || got.Y != 6 {
			t.Fatalf("drag release = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tab switch did not release the pointer on its original target")
	}
}

func TestBrowserSurfaceQueuedInputReachesTypedOperator(t *testing.T) {
	operator := &fakeBrowserOperator{events: make(chan BrowserOperatorInput, 4)}
	target := browserctl.Target{BrowserID: "browser", TabID: "tab", TargetID: "target"}
	surface := &BrowserSurface{operator: operator, target: target, input: newBrowserInputQueue(8), done: make(chan struct{})}
	go surface.runInputs()
	t.Cleanup(func() { close(surface.done) })
	for _, kind := range []BrowserInputKind{BrowserPointerDown, BrowserPointerMove, BrowserPointerUp} {
		if !surface.Input(BrowserInput{Kind: kind, X: 10, Y: 20}) {
			t.Fatalf("input %v rejected", kind)
		}
	}
	for _, kind := range []string{"pointer-down", "pointer-move", "pointer-up"} {
		select {
		case event := <-operator.events:
			if event.Kind != kind || event.TargetID != target.TargetID {
				t.Fatalf("got %+v, want %s on %s", event, kind, target.TargetID)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}
