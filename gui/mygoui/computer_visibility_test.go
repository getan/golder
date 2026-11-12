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
	if !tt.HasText("manual window list shows the current desktop") || !tt.HasText("without switching Spaces") || !tt.HasText("restore it and refresh") {
		t.Fatalf("missing visibility guidance: %q", tt.Texts())
	}
}
