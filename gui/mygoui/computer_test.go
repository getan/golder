package mygoui

import (
	"context"
	"image"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/computeruse"
)

type computerUIBackend struct{}

func (computerUIBackend) Validate(context.Context, computeruse.Target, bool) error { return nil }
func (computerUIBackend) Close() error                                             { return nil }

func TestComputerPickerShowsExplicitScopeAndControlChoice(t *testing.T) {
	a := testApp()
	a.computerPickerOpen = true
	a.computerTargets = []computeruse.Target{{ID: "opaque", Application: "Calculator", Window: "Calculator"}}
	a.computerSelected = 0
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Computer Use", "Calculator · Calculator", "View only", "Allow control", "Cancel"} {
		if !tt.HasText(want) {
			t.Fatalf("picker is missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestMacAppBundlePath(t *testing.T) {
	bundle := filepath.Join("Applications", "Golder GUI.app")
	executable := filepath.Join(bundle, "Contents", "MacOS", "Golder GUI")
	if got := macAppBundlePath(executable); got != bundle {
		t.Fatalf("bundle=%q want %q", got, bundle)
	}
	if got := macAppBundlePath(filepath.Join("tmp", "golder-gui")); got != "" {
		t.Fatalf("unbundled development binary produced a bundle: %q", got)
	}
}

func TestMacComputerPermissionsAreAvailableInSettings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS settings")
	}
	a := testApp()
	a.settingsOpen, a.settingsTab = true, 1
	a.computerPermission = computeruse.PermissionState{ScreenCapture: "denied", Accessibility: "denied"}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"macOS Computer Use permissions", "Request Accessibility", "Request Screen Recording", "Accessibility settings", "Screen Recording settings", "Show Golder GUI.app in Finder", "Refresh system permissions"} {
		if !tt.HasText(want) {
			t.Fatalf("permissions settings are missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestComputerPickerRequiresLocalPreviewConfirmation(t *testing.T) {
	a := testApp()
	a.computerPickerOpen = true
	a.computerTargets = []computeruse.Target{{ID: "opaque", Application: "Calculator", Window: "Main"}}
	a.computerSelected = 0
	a.computerCandidate = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 8, 8)))
	a.computerCandidateID, a.computerCandidateTarget = "candidate", "opaque"
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Selected: Calculator · Main", "Confirm same window", "Cancel"} {
		if !tt.HasText(want) {
			t.Fatalf("confirmation missing %q; texts=%q", want, tt.Texts())
		}
	}
	if tt.HasText("View only") || tt.HasText("Allow control") {
		t.Fatal("grant controls must not bypass preview confirmation")
	}
}

func TestComputerDrawerShowsTargetAndTakeover(t *testing.T) {
	a := testApp()
	session := computeruse.New(computerUIBackend{})
	if err := session.Start(context.Background(), computeruse.Target{ID: "opaque", Application: "Calculator", Window: "Calculator"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	a.computerSession, a.computerOpen = session, true
	tt := ui.NewTester(a.view, 1400, 800)
	for _, want := range []string{"Calculator · Calculator", "Pause / take over", "Stop", "Agent control permitted"} {
		if !tt.HasText(want) {
			t.Fatalf("computer drawer is missing %q; texts=%q", want, tt.Texts())
		}
	}
	if err := tt.Click("Pause / take over"); err != nil || !session.Status().Paused {
		t.Fatalf("pause failed: paused=%t err=%v", session.Status().Paused, err)
	}
}

func TestComputerPanelOpensAndReplacesOtherRightPanels(t *testing.T) {
	a := testApp()
	session := computeruse.New(computerUIBackend{})
	if err := session.Start(context.Background(), computeruse.Target{ID: "window", Application: "Calculator"}, computeruse.Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	a.computerSession = session
	a.filesOpen, a.changesOpen, a.browserPanelOpen = true, true, true
	a.showComputerPanel()
	if !a.computerOpen || a.filesOpen || a.changesOpen || a.browserPanelOpen {
		t.Fatalf("right panels: computer=%t files=%t changes=%t browser=%t", a.computerOpen, a.filesOpen, a.changesOpen, a.browserPanelOpen)
	}
	tt := ui.NewTester(a.view, 1400, 800)
	if !tt.HasText("Computer Use") || !tt.HasText("Observation only") || !tt.HasText("Window preview unavailable; semantic access only.") {
		t.Fatalf("computer panel not visible: %q", tt.Texts())
	}
}

func TestComputerPickerDistinguishesDuplicateWindowNames(t *testing.T) {
	a := testApp()
	a.computerPickerOpen = true
	a.computerTargets = []computeruse.Target{
		{ID: "one", Application: "Calculator", Window: "Main", Detail: "PID 42 · Window 1"},
		{ID: "two", Application: "Calculator", Window: "Main", Detail: "PID 42 · Window 2"},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Calculator · Main · PID 42 · Window 1", "Calculator · Main · PID 42 · Window 2"} {
		if !tt.HasText(want) {
			t.Fatalf("ambiguous window label missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestComputerResolutionPreselectsOnlyUniqueTarget(t *testing.T) {
	a := testApp()
	a.computerSuggested = "Calculator"
	a.status.Permission = "read-only" // no native window is needed in this test
	unique := computeruse.ApplicationResolution{Matched: true, Targets: []computeruse.Target{{ID: "one", Application: "计算器"}}}
	a.applyComputerResolution(unique)
	if a.computerSelected != 0 || !a.computerMatched {
		t.Fatalf("unique target was not selected: %+v", a.computerTargets)
	}
	a.applyComputerResolution(computeruse.ApplicationResolution{Matched: true, Targets: []computeruse.Target{{ID: "one"}, {ID: "two"}}})
	if a.computerSelected != -1 {
		t.Fatal("ambiguous windows were selected automatically")
	}
	a.applyComputerResolution(computeruse.ApplicationResolution{Targets: []computeruse.Target{{ID: "other", Application: "Notes"}}})
	if a.computerSelected != -1 || a.computerMatched {
		t.Fatal("an unrelated window was selected automatically")
	}
}

func TestComputerPickerDistinguishesInstalledApplications(t *testing.T) {
	a := testApp()
	a.computerPickerOpen = true
	a.computerApplications = []computeruse.Application{
		{ID: "one", Name: "Calculator", Detail: "/Applications/Calculator.app"},
		{ID: "two", Name: "Calculator", Detail: "/System/Applications/Calculator.app"},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Calculator · /Applications/Calculator.app", "Calculator · /System/Applications/Calculator.app"} {
		if !tt.HasText(want) {
			t.Fatalf("application choice missing %q; texts=%q", want, tt.Texts())
		}
	}
}
