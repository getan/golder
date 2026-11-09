package mygoui

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMacComputerPickerExplainsVisibleWindows(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS window inventory")
	}
	a := testApp()
	a.computerPickerOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Only windows on the current desktop are listed") || !tt.HasText("Restore minimized windows") {
		t.Fatalf("missing visibility guidance: %q", tt.Texts())
	}
}
