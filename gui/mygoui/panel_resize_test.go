package mygoui

import (
	"fmt"
	"strings"
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

func TestWorkspaceResizeKeepsComposerControlsVisible(t *testing.T) {
	for _, width := range []int{860, 1200} {
		for _, scale := range []float32{1, 1.35} {
			for _, sidebarClosed := range []bool{false, true} {
				name := fmt.Sprintf("width=%d/scale=%.2f/sidebarClosed=%t", width, scale, sidebarClosed)
				t.Run(name, func(t *testing.T) {
					a := testApp()
					a.display.FontScale = scale
					a.sidebarClosed = sidebarClosed
					a.sidebarSize = 10000
					a.rightSplit = 1
					a.browserPanelOpen = true
					a.browserState = &browserWindowState{title: "Workspace browser"}
					a.status.Model = strings.Repeat("very-long-model-name-", 8)
					a.draft = "ready"
					tt := ui.NewTester(a.view, width, 800)
					tt.Frame()
					tt.Frame()
					send, okSend := tt.Find("Send message")
					browser, okBrowser := tt.Find("Workspace browser")
					if !okSend || !okBrowser || send.X+send.W > browser.X-20*scale {
						t.Fatalf("send button is covered: send=%+v browser=%+v sidebar=%v rightSplit=%v", send, browser, a.sidebarSize, a.rightSplit)
					}
					if send.W < 33*scale {
						t.Fatalf("send button was squeezed: %+v", send)
					}
					a.status.Running = true
					tt.Frame()
					stop, okStop := tt.Find("Stop generation")
					if !okStop || stop.X+stop.W > browser.X-20*scale || stop.W < 33*scale {
						t.Fatalf("stop button is covered or squeezed: stop=%+v browser=%+v", stop, browser)
					}
				})
			}
		}
	}
}

func TestRightDividerCannotDragAcrossComposer(t *testing.T) {
	a := testApp()
	a.sidebarClosed = true
	a.browserPanelOpen = true
	a.browserState = &browserWindowState{title: "Workspace browser"}
	a.status.Model = strings.Repeat("very-long-model-name-", 8)
	a.draft = "ready"
	tt := ui.NewTester(a.view, 860, 800)
	tt.Frame()
	divider, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("right divider not found")
	}
	x, y := divider.X+divider.W/2, divider.Y+divider.H/2
	tt.Press(x, y)
	tt.Move(55, y)
	tt.Release(55, y)
	tt.Frame()
	send, okSend := tt.Find("Send message")
	browser, okBrowser := tt.Find("Workspace browser")
	if !okSend || !okBrowser || send.X+send.W > browser.X-20 {
		t.Fatalf("drag hid send button: send=%+v browser=%+v split=%v", send, browser, a.rightSplit)
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
