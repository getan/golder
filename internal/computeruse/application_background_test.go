package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

type backgroundWindowFixture struct {
	revealApplicationFixture
	windows     []Target
	afterLaunch []Target
	lookupError error
	lookupCalls int
}

func (f *backgroundWindowFixture) ApplicationWindows(_ context.Context, _ Application) ([]Target, error) {
	f.lookupCalls++
	if f.lookupError != nil {
		return nil, f.lookupError
	}
	if len(f.launched) != 0 {
		return f.afterLaunch, nil
	}
	return f.windows, nil
}

func TestResolveExistingBackgroundWindowWithoutSpaceSwitchOrRelaunch(t *testing.T) {
	app := Application{ID: "issued", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &backgroundWindowFixture{
		revealApplicationFixture: revealApplicationFixture{applicationFixture: applicationFixture{apps: []Application{app}}},
		windows: []Target{
			{ID: "wrong", Application: "Calculator", ApplicationID: "/other/Calculator.app"},
			{ID: "right", Application: "计算器", ApplicationID: app.Identity},
		},
	}
	resolved, err := ResolveApplication(context.Background(), f, "计算器")
	if err != nil || !resolved.Matched || len(resolved.Targets) != 1 || resolved.Targets[0].ID != "right" || len(f.launched) != 0 || len(f.revealed) != 0 {
		t.Fatalf("background lookup: result=%+v launches=%d reveals=%d err=%v", resolved, len(f.launched), len(f.revealed), err)
	}
}

func TestBackgroundWindowLookupPreservesAmbiguity(t *testing.T) {
	app := Application{ID: "issued", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &backgroundWindowFixture{
		revealApplicationFixture: revealApplicationFixture{applicationFixture: applicationFixture{apps: []Application{app}}},
		windows:                  []Target{{ID: "one", ApplicationID: app.Identity}, {ID: "two", ApplicationID: app.Identity}},
	}
	resolved, err := ResolveApplication(context.Background(), f, "Calculator")
	if err != nil || len(resolved.Targets) != 2 || len(f.launched) != 0 || len(f.revealed) != 0 {
		t.Fatalf("ambiguous windows must not be chosen or raised: result=%+v err=%v", resolved, err)
	}
}

func TestBackgroundWindowLookupFailureNeverLaunches(t *testing.T) {
	app := Application{ID: "issued", Name: "Calculator", Identity: "/apps/Calculator.app"}
	denied := errors.New("catalog unavailable")
	f := &backgroundWindowFixture{
		revealApplicationFixture: revealApplicationFixture{applicationFixture: applicationFixture{apps: []Application{app}}},
		lookupError:              denied,
	}
	_, err := ResolveApplication(context.Background(), f, "Calculator")
	if !errors.Is(err, denied) || len(f.launched) != 0 || len(f.revealed) != 0 {
		t.Fatalf("lookup failure triggered app activation: err=%v launches=%d reveals=%d", err, len(f.launched), len(f.revealed))
	}
}

func TestBackgroundDiscoveryAfterLaunchNeverReveals(t *testing.T) {
	app := Application{ID: "issued", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &backgroundWindowFixture{afterLaunch: []Target{{ID: "window", ApplicationID: app.Identity}}}
	resolved, err := launchAndFindApplication(context.Background(), f, app, time.Millisecond)
	if err != nil || len(resolved.Targets) != 1 || len(f.launched) != 1 || len(f.revealed) != 0 {
		t.Fatalf("background launch: result=%+v launches=%d reveals=%d err=%v", resolved, len(f.launched), len(f.revealed), err)
	}
}
