package computeruse

import (
	"context"
	"testing"
	"time"
)

func TestOrderWorkingWindowsKeepsFrontToBackOrder(t *testing.T) {
	helper := Target{ID: "helper", Width: 33, Height: 10}
	main := Target{ID: "main", Width: 1200, Height: 800}
	unknown := Target{ID: "unknown"}
	got := orderWorkingWindows([]Target{helper, main, unknown})
	for i, want := range []string{"main", "unknown", "helper"} {
		if got[i].ID != want {
			t.Fatalf("order = %v, want main, unknown, helper", []string{got[0].ID, got[1].ID, got[2].ID})
		}
	}
}

func TestHelperWindowNeverTreatsUnknownGeometryAsSmall(t *testing.T) {
	for _, target := range []Target{
		{ID: "unknown"},
		{ID: "width only", Width: 1200},
		{ID: "main", Width: 1200, Height: 800},
	} {
		if helperWindow(target) {
			t.Fatalf("%q was treated as a helper window", target.ID)
		}
	}
	if !helperWindow(Target{ID: "indicator", Width: 33, Height: 10}) {
		t.Fatal("a 33x10 window was treated as a working surface")
	}
}

// stagedLaunchFixture publishes an indicator window first and the real window
// on later polls, as a Chromium-based app does while it starts.
type stagedLaunchFixture struct {
	applicationFixture
	calls   int
	working bool
}

func (f *stagedLaunchFixture) Targets(context.Context) ([]Target, error) {
	f.calls++
	helper := Target{ID: "helper", Application: "Calculator", ApplicationID: "/apps/Calculator.app", Width: 33, Height: 10}
	if !f.working || f.calls < 2 {
		return []Target{helper}, nil
	}
	return []Target{helper, {ID: "main", Application: "Calculator", ApplicationID: "/apps/Calculator.app", Width: 1200, Height: 800}}, nil
}

func TestLaunchWaitsForAWorkingWindow(t *testing.T) {
	previous := helperWindowSettle
	helperWindowSettle = 500 * time.Millisecond
	t.Cleanup(func() { helperWindowSettle = previous })

	f := &stagedLaunchFixture{working: true}
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	started := time.Now()
	resolution, err := LaunchAndFindApplication(context.Background(), f, app)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= helperWindowSettle {
		t.Fatalf("launch settled after %v instead of waiting for the real window", elapsed)
	}
	if len(resolution.Targets) != 1 || resolution.Targets[0].ID != "main" {
		t.Fatalf("targets = %+v", resolution.Targets)
	}
}

func TestLaunchSettlesForAnIndicatorWindowAfterTheGracePeriod(t *testing.T) {
	previous := helperWindowSettle
	helperWindowSettle = 30 * time.Millisecond
	t.Cleanup(func() { helperWindowSettle = previous })

	f := &stagedLaunchFixture{}
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	resolution, err := LaunchAndFindApplication(context.Background(), f, app)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Targets) != 1 || resolution.Targets[0].ID != "helper" {
		t.Fatalf("targets = %+v", resolution.Targets)
	}
}
