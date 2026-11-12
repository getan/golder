package computeruse

import (
	"strings"
	"testing"
)

func TestPaneMouseAcceptsOnlyBoundedLeftPress(t *testing.T) {
	for _, input := range []string{"<0;12;3M", "<0;1;1M"} {
		var filter paneInputFilter
		clicks := 0
		for _, r := range "\x1b[" + input {
			if _, ok := filter.accept(r); ok {
				t.Fatal("mouse sequence leaked into prompt")
			}
			if filter.mouse != nil {
				clicks++
			}
		}
		if clicks != 1 {
			t.Fatalf("mouse press not decoded once: %q", input)
		}
	}
	for _, input := range []string{"<0;12;3m", "<2;12;3M", "<32;12;3M", "<64;12;3M", "<0;0;3M", "<0;1001;3M", "<0;1;2;3M", "?2026;1$y"} {
		if parsePaneMouse(input) != nil {
			t.Fatalf("unsafe mouse input accepted: %q", input)
		}
	}
}

func TestPanePreviewPointerRequiresHumanControl(t *testing.T) {
	state := paneViewerState{graphics: true, status: Status{Active: true, Paused: true, OperatorControl: true}, frame: &Frame{ID: "frame", Width: 800, Height: 400}}
	x, y, ok := panePreviewPoint(state, paneMouseClick{column: 1, row: 2}, 80, 24)
	if !ok || x != 5 || y != 10 {
		t.Fatalf("incorrect click mapping: %v %v %t", x, y, ok)
	}
	for _, click := range []paneMouseClick{{1, 1}, {1, 22}, {81, 2}, {0, 2}} {
		if _, _, ok := panePreviewPoint(state, click, 80, 24); ok {
			t.Fatalf("non-image click accepted: %+v", click)
		}
	}
	state.status.OperatorControl = false
	if _, _, ok := panePreviewPoint(state, paneMouseClick{1, 2}, 80, 24); ok {
		t.Fatal("click allowed during Agent control")
	}
	if got := paneInputDisplay("type 私密文字"); strings.Contains(got, "私密文字") || !strings.Contains(got, "•••") {
		t.Fatalf("typed value echoed: %q", got)
	}
}
