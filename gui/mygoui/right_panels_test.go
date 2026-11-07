package mygoui

import (
	"context"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func TestRightPanelPriorityAndManualSelection(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		computer, browser, terminal bool
		preferred                   rightPanel
		want                        []rightPanel
	}{
		{"none", false, false, false, rightPanelNone, nil},
		{"terminal", false, false, true, rightPanelNone, []rightPanel{rightPanelTerminal}},
		{"browser and terminal", false, true, true, rightPanelNone, []rightPanel{rightPanelBrowser, rightPanelTerminal}},
		{"computer and terminal", true, false, true, rightPanelNone, []rightPanel{rightPanelComputer, rightPanelTerminal}},
		{"default priority", true, true, true, rightPanelNone, []rightPanel{rightPanelComputer, rightPanelBrowser}},
		{"manual terminal", true, true, true, rightPanelTerminal, []rightPanel{rightPanelComputer, rightPanelTerminal}},
		{"manual browser", true, true, true, rightPanelBrowser, []rightPanel{rightPanelComputer, rightPanelBrowser}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.computerOpen, a.browserPanelOpen, a.terminalOpen, a.rightPreferred = tc.computer, tc.browser, tc.terminal, tc.preferred
			if got := a.rightPanels(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rightPanels = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRightPanelUserSwitchAndAutomaticFallback(t *testing.T) {
	a := testApp()
	a.computerOpen, a.browserPanelOpen, a.terminalOpen = true, true, true
	a.browserState = &browserWindowState{title: "Browser preview"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Terminal"); err != nil || !a.rightPanelVisible(rightPanelTerminal) || !a.browserPanelOpen || a.browserState == nil {
		t.Fatalf("terminal did not displace browser without closing it: err=%v", err)
	}
	tt.Frame()
	if err := tt.Click("Browser"); err != nil || !a.rightPanelVisible(rightPanelBrowser) || !a.terminalOpen {
		t.Fatalf("browser did not displace terminal without closing it: err=%v", err)
	}
	a.hideComputerPanel()
	if got := a.rightPanels(); !reflect.DeepEqual(got, []rightPanel{rightPanelBrowser, rightPanelTerminal}) {
		t.Fatalf("closing computer did not restore terminal: %v", got)
	}
}

func TestAutomaticBrowserOpenRespectsManualDrawerAndPriority(t *testing.T) {
	a := testApp()
	a.filesOpen, a.computerOpen, a.terminalOpen = true, true, true
	a.rightPreferred = rightPanelTerminal
	a.openBrowserPreview(appsession.BrowserPreview{Title: "New browser"})
	if !a.filesOpen || !a.computerOpen || !a.terminalOpen || !a.browserPanelOpen || a.rightPreferred != rightPanelNone {
		t.Fatal("automatic browser open displaced the manual drawer or retained a stale preference")
	}
	a.filesOpen = false
	if got := a.rightPanels(); !reflect.DeepEqual(got, []rightPanel{rightPanelComputer, rightPanelBrowser}) {
		t.Fatalf("automatic browser open did not restore priority: %v", got)
	}
}

func TestBrowserDiscoveryDoesNotInterruptManualDrawer(t *testing.T) {
	for _, files := range []bool{true, false} {
		a := testApp()
		a.filesOpen, a.changesOpen, a.browserAutoOpen = files, !files, true
		a.applyBrowserDiscovery([]appsession.BrowserPreview{{Socket: "/tmp/a.sock"}, {Socket: "/tmp/b.sock"}})
		if a.browserOpen || len(a.browsers) != 2 || a.filesOpen != files || a.changesOpen == files {
			t.Fatal("browser discovery interrupted the manual drawer or lost discovered instances")
		}
	}
}

func TestManualDrawerPreservesViewsAndUnsavedFile(t *testing.T) {
	a := testApp()
	a.browserPanelOpen, a.terminalOpen = true, true
	state := &browserWindowState{title: "Browser preview"}
	a.browserState = state
	a.filesOpen, a.fileSelected = true, "main.go"
	a.filePreview = appsession.ProjectFilePreview{Path: "main.go", Text: "package main\n"}
	a.initFileEditor()
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("File editor"); err != nil {
		t.Fatal(err)
	}
	tt.Type("x")
	if !a.fileHasChanges() {
		t.Fatal("file edit did not become dirty")
	}
	if err := tt.Click("Files"); err != nil || a.filesOpen {
		t.Fatalf("could not close Files: %v", err)
	}
	if a.browserState != state || !a.rightPanelVisible(rightPanelBrowser) || !a.rightPanelVisible(rightPanelTerminal) || !a.fileHasChanges() {
		t.Fatal("closing Files lost the background views or unsaved editor")
	}
	if err := tt.Click("Files"); err != nil || !a.filesOpen || !a.fileHasChanges() {
		t.Fatalf("reopening Files lost unsaved edits: %v", err)
	}
}

func TestChangesCloseCancelsDiffAndRevealsUnderlyingViews(t *testing.T) {
	a := testApp()
	a.browserPanelOpen, a.changesOpen, a.diffLoading = true, true, true
	ctx, cancel := context.WithCancel(context.Background())
	a.diffCancel = cancel
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Refresh Changes") || !tt.HasText("Close Changes") {
		t.Fatalf("Changes icon actions missing: %q", tt.Texts())
	}
	if err := tt.Click("Close Changes"); err != nil || a.changesOpen || a.diffLoading || ctx.Err() == nil || !a.rightPanelVisible(rightPanelBrowser) {
		t.Fatalf("closing Changes did not cancel and reveal: err=%v", err)
	}
}

func TestTwoRightPanelsAreStacked(t *testing.T) {
	a := testApp()
	a.sidebarClosed = true
	a.computerOpen, a.browserPanelOpen = true, true
	a.browserState = &browserWindowState{title: "Unique browser title"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	computer, okComputer := tt.Find("Stopped")
	browser, okBrowser := tt.Find("Unique browser title")
	if !okComputer || !okBrowser || computer.Y >= browser.Y || browser.X < computer.X-2 || browser.X > computer.X+100 {
		t.Fatalf("views are not vertically stacked: computer=%+v browser=%+v", computer, browser)
	}
	if a.rightStackSplit != 50 {
		t.Fatalf("unexpected initial stack split: %v", a.rightStackSplit)
	}
}

func TestRightStackDividerResizesAndPersists(t *testing.T) {
	a := testApp()
	a.computerOpen, a.browserPanelOpen = true, true
	a.browserState = &browserWindowState{title: "Browser"}
	tt := ui.NewTester(func(c *ui.Context) { a.rightPanelView(c) }, 600, 800)
	tt.Frame()
	divider, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("stack divider not found")
	}
	x, y := divider.X+divider.W/2, divider.Y+divider.H/2
	tt.Press(x, y)
	tt.Move(x, y+100)
	tt.Release(x, y+100)
	tt.Frame()
	if a.rightStackSplit <= 50 || a.rightStackSplit > 70 {
		t.Fatalf("vertical divider did not resize: %v", a.rightStackSplit)
	}
	split := a.rightStackSplit
	a.filesOpen = true
	tt.Frame()
	a.filesOpen = false
	tt.Frame()
	if a.rightStackSplit != split {
		t.Fatal("manual drawer lost the automation split")
	}
}
