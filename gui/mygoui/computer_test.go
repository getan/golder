package mygoui

import (
	"context"
	"image"
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
	for _, want := range []string{"Calculator · Calculator", "Pause / take over", "Stop", "Agent control permitted · actions require confirmation"} {
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

func TestSuggestedTargetsOnlyPreselectUniqueExactApplication(t *testing.T) {
	targets := []computeruse.Target{{ID: "a", Application: "Calculator"}, {ID: "b", Application: "Notes"}, {ID: "c", Application: "Calculator"}}
	filtered, selected := matchComputerTargets("Calculator", targets)
	if len(filtered) != 2 || selected != -1 || filtered[0].ID != "a" || filtered[1].ID != "c" {
		t.Fatalf("ambiguous selection = %+v, %d", filtered, selected)
	}
	filtered, selected = matchComputerTargets("notes", targets)
	if len(filtered) != 3 || selected != 1 {
		t.Fatalf("unique selection = %+v, %d", filtered, selected)
	}
	filtered, selected = matchComputerTargets("missing", targets)
	if len(filtered) != 3 || selected != -1 {
		t.Fatalf("missing selection = %+v, %d", filtered, selected)
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
