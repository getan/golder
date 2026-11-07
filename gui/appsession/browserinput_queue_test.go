package appsession

import (
	"testing"

	"github.com/getan/golder/internal/browserctl"
)

func TestBrowserInputQueuePreservesBoundariesAndReservesRelease(t *testing.T) {
	q := newBrowserInputQueue(4)
	target := browserctl.Target{BrowserID: "b", TabID: "t", TargetID: "d"}
	push := func(kind BrowserInputKind, x float64) bool {
		return q.push(surfaceInput{target: target, event: BrowserInput{Kind: kind, X: x}})
	}
	if !push(BrowserPointerDown, 1) || !push(BrowserPointerMove, 2) || !push(BrowserPointerMove, 3) || !push(BrowserText, 0) {
		t.Fatal("failed to queue input or coalesce moves")
	}
	if push(BrowserKey, 0) {
		t.Fatal("full queue silently accepted a boundary event")
	}
	if !push(BrowserPointerUp, 4) {
		t.Fatal("reserved release slot was unavailable")
	}
	for _, want := range []struct {
		kind BrowserInputKind
		x    float64
	}{{BrowserPointerDown, 1}, {BrowserPointerMove, 3}, {BrowserText, 0}, {BrowserPointerUp, 4}} {
		got, ok := q.pop()
		if !ok || got.event.Kind != want.kind || got.event.X != want.x {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestBrowserInputQueueDoesNotCoalesceAcrossTargets(t *testing.T) {
	q := newBrowserInputQueue(4)
	for _, id := range []string{"a", "b"} {
		if !q.push(surfaceInput{target: browserctl.Target{TabID: id}, event: BrowserInput{Kind: BrowserPointerMove}}) {
			t.Fatal("could not enqueue move")
		}
	}
	first, ok1 := q.pop()
	second, ok2 := q.pop()
	if !ok1 || !ok2 || first.target.TabID != "a" || second.target.TabID != "b" {
		t.Fatalf("moves lost target identity: %+v %+v", first, second)
	}
}

func TestBrowserSurfaceInputReportsQueueOverload(t *testing.T) {
	s := &BrowserSurface{input: newBrowserInputQueue(2)}
	if !s.Input(BrowserInput{Kind: BrowserText, Text: "a"}) {
		t.Fatal("first event rejected")
	}
	if s.Input(BrowserInput{Kind: BrowserKey, Key: "Enter"}) {
		t.Fatal("full queue reported accepted input")
	}
	_, _, err := s.State()
	if err == nil {
		t.Fatal("queue overload was not reported")
	}
}

func TestBrowserSurfaceReportsUnavailableNativePointer(t *testing.T) {
	s := &BrowserSurface{input: newBrowserInputQueue(4)}
	if s.NativePointerAvailable() || s.Input(BrowserInput{Kind: BrowserPointerDown}) {
		t.Fatal("legacy host accepted native pointer input")
	}
	_, _, err := s.State()
	if err == nil {
		t.Fatal("missing native pointer capability was not reported")
	}
}
