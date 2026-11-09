package mygoui

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestWindowsDesktopMenuRoutesExitThroughWindowClose(t *testing.T) {
	closed := 0
	items := desktopMenuItems("windows", func() { closed++ })
	if len(items) != 3 || items[0].Label != "File" || items[1].Role != mygo.RoleEditMenu || items[2].Role != mygo.RoleWindowMenu {
		t.Fatalf("Windows menu = %+v", items)
	}
	exit := items[0].Submenu[0]
	if exit.Role == mygo.RoleQuit || exit.Click == nil {
		t.Fatal("Exit must use the guarded window-close path, not App.Quit")
	}
	exit.Click(exit, nil)
	if closed != 1 {
		t.Fatalf("close callback invoked %d times", closed)
	}
	for _, item := range items {
		if item.Role == mygo.RoleAppMenu {
			t.Fatal("Windows retained a macOS-only App menu")
		}
	}
}

func TestMacAndLinuxMenusUnchanged(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		items := desktopMenuItems(goos, func() { t.Fatal("unexpected close") })
		if len(items) != 3 || items[0].Role != mygo.RoleAppMenu || items[1].Role != mygo.RoleEditMenu || items[2].Role != mygo.RoleWindowMenu {
			t.Fatalf("%s menu changed: %+v", goos, items)
		}
	}
}

func TestWindowsFileDialogDefersCloseUntilDialogReturns(t *testing.T) {
	var state fileDialogState
	if state.deferClose() {
		t.Fatal("ordinary close was deferred with no dialog")
	}
	if !state.begin() || state.begin() {
		t.Fatal("a second native picker may open during the first")
	}
	if !state.deferClose() || !state.deferClose() {
		t.Fatal("close must remain deferred while a picker owns the native loop")
	}
	if state.begin() {
		t.Fatal("pending close admitted a new picker")
	}
	if !state.finish() || state.deferClose() {
		t.Fatal("dialog completion did not release the pending close")
	}
	if state.finish() {
		t.Fatal("one close request was replayed")
	}
}

func TestWindowsCanceledFileDialogDoesNotCloseWindow(t *testing.T) {
	var state fileDialogState
	if !state.begin() || state.finish() || !state.begin() || state.finish() {
		t.Fatal("completing a picker without a close request should keep the window usable")
	}
}

func TestFileDialogsRefuseMissingOrShuttingDownOwner(t *testing.T) {
	a := testApp()
	if _, _, ok := a.beginFileDialog(); ok {
		t.Fatal("picker allowed without an owner window")
	}
	a.terminalShuttingDown = true
	if _, _, ok := a.beginFileDialog(); ok {
		t.Fatal("picker allowed during shutdown")
	}
}
