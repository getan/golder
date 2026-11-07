package mygoui

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPermissionSettingsDefersApprovalModeToComposer(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.permissionSettingsView, 900, 700)
	if !tt.HasText("Change the session's approval mode using the hand button in the composer.") {
		t.Fatalf("settings should explain where to change approval mode: %q", tt.Texts())
	}
	for _, mode := range []string{"read-only", "ask", "auto", "full-access"} {
		if _, found := tt.Find(mode); found {
			t.Fatalf("settings still exposes duplicate approval option %q", mode)
		}
	}
	if runtime.GOOS == "darwin" && !tt.HasText("macOS Computer Use permissions") {
		t.Fatal("system permission settings must remain available")
	}
}

func TestComposerApprovalButtonStillShowsModes(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Approval mode"); err != nil || !a.permOpen {
		t.Fatalf("hand button did not open approval settings: open=%t err=%v", a.permOpen, err)
	}
	for _, mode := range []string{"read-only", "ask", "auto", "full-access"} {
		if _, found := tt.Find(mode); !found {
			t.Fatalf("approval picker missing %q: %q", mode, tt.Texts())
		}
	}
}
