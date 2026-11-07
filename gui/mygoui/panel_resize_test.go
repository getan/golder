package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRightPanelsAndTerminalResize(t *testing.T) {
	tests := []struct {
		name string
		open func(*App, bool)
	}{
		{"files", func(a *App, open bool) { a.filesOpen = open }},
		{"changes", func(a *App, open bool) { a.changesOpen = open }},
		{"computer", func(a *App, open bool) { a.computerOpen = open }},
		{"browser", func(a *App, open bool) { a.browserPanelOpen = open }},
		{"terminal", func(a *App, open bool) { a.terminalOpen = open }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.sidebarClosed = true
			tc.open(a, true)
			tt := ui.NewTester(a.view, 1200, 800)
			tt.Frame()
			before := a.rightSplit
			divider, ok := tt.Find("Divider")
			if !ok {
				t.Fatal("resize divider not found")
			}
			x, y := divider.X+divider.W/2, divider.Y+divider.H/2
			tt.Press(x, y)
			tt.Move(x-100, y)
			tt.Release(x-100, y)
			tt.Frame()
			after := a.rightSplit
			if after >= before || after < 30 || after > 70 {
				t.Fatalf("drag did not resize within bounds: before=%v after=%v", before, after)
			}
			tc.open(a, false)
			tt.Frame()
			tc.open(a, true)
			tt.Frame()
			if a.rightSplit != after {
				t.Fatal("closing and reopening lost the panel width")
			}
		})
	}
}

func TestPanelSplitBounds(t *testing.T) {
	for _, tc := range []struct{ input, want float32 }{{0, 60}, {1, 30}, {100, 70}, {52, 52}} {
		got := tc.input
		clampPanelSplit(&got, 60)
		if got != tc.want {
			t.Fatalf("clamp(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestBrowserViewportTracksDivider(t *testing.T) {
	a := testApp()
	a.sidebarClosed = true
	a.browserPanelOpen = true
	a.browserState = &browserWindowState{title: "Example"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	before := a.browserState.viewW
	divider, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("browser divider not found")
	}
	x, y := divider.X+divider.W/2, divider.Y+divider.H/2
	tt.Press(x, y)
	tt.Move(x-100, y)
	tt.Release(x-100, y)
	tt.Frame()
	if a.browserState.viewW <= before {
		t.Fatalf("browser viewport did not grow: before=%v after=%v", before, a.browserState.viewW)
	}
}
