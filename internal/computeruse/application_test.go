package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

type applicationFixture struct {
	targets  []Target
	apps     []Application
	launched []Application
	after    []Target
}

func (f *applicationFixture) Targets(context.Context) ([]Target, error) {
	if len(f.launched) > 0 {
		return f.after, nil
	}
	return f.targets, nil
}
func (*applicationFixture) Validate(context.Context, Target, bool) error { return nil }
func (*applicationFixture) Close() error                                 { return nil }
func (f *applicationFixture) Applications(context.Context, string) ([]Application, error) {
	return f.apps, nil
}
func (f *applicationFixture) LaunchApplication(_ context.Context, app Application) error {
	f.launched = append(f.launched, app)
	return nil
}

func TestResolveApplicationReusesUniqueWindow(t *testing.T) {
	f := &applicationFixture{targets: []Target{{ID: "other", Application: "Notes"}, {ID: "calc", Application: "Calculator"}}}
	resolution, err := ResolveApplication(context.Background(), f, "calculator")
	if err != nil || !resolution.Matched || len(resolution.Targets) != 1 || resolution.Targets[0].ID != "calc" || len(f.launched) != 0 {
		t.Fatalf("resolution=%+v launched=%+v err=%v", resolution, f.launched, err)
	}
}

func TestResolveApplicationReusesLocalizedWindowsByIdentity(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &applicationFixture{apps: []Application{app}, targets: []Target{
		{ID: "other", Application: "Calculator", ApplicationID: "/other/Calculator.app"},
		{ID: "one", Application: "Calculator", ApplicationID: app.Identity},
		{ID: "two", Application: "Calculator", ApplicationID: app.Identity},
	}}
	resolution, err := ResolveApplication(context.Background(), f, "计算器")
	if err != nil || !resolution.Matched || len(resolution.Targets) != 2 || resolution.Targets[0].ID != "one" || resolution.Targets[1].ID != "two" || len(f.launched) != 0 {
		t.Fatalf("resolution=%+v launched=%+v err=%v", resolution, f.launched, err)
	}
}

func TestResolveApplicationLaunchesOnlyUniqueCandidate(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &applicationFixture{apps: []Application{app}, after: []Target{
		{ID: "wrong", Application: "Calculator", ApplicationID: "/other/Calculator.app"},
		{ID: "right", Application: "计算器", ApplicationID: app.Identity},
	}}
	resolution, err := ResolveApplication(context.Background(), f, "Calculator")
	if err != nil || !resolution.Matched || len(f.launched) != 1 || len(resolution.Targets) != 1 || resolution.Targets[0].ID != "right" {
		t.Fatalf("resolution=%+v launched=%+v err=%v", resolution, f.launched, err)
	}
}

func TestResolveApplicationReturnsAmbiguityWithoutLaunching(t *testing.T) {
	f := &applicationFixture{apps: []Application{{ID: "a", Name: "Calculator"}, {ID: "b", Name: "Calculator"}}}
	resolution, err := ResolveApplication(context.Background(), f, "Calculator")
	if err != nil || resolution.Matched || len(resolution.Applications) != 2 || len(f.launched) != 0 {
		t.Fatalf("resolution=%+v launched=%+v err=%v", resolution, f.launched, err)
	}
}

func TestApplicationLaunchGuardRunsAfterDiscovery(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &applicationFixture{apps: []Application{app}}
	denied := errors.New("permission changed")
	ctx := WithApplicationLaunchGuard(context.Background(), func() error { return denied })
	_, err := ResolveApplication(ctx, f, "Calculator")
	if !errors.Is(err, denied) || len(f.launched) != 0 {
		t.Fatalf("launch guard failed: launched=%+v err=%v", f.launched, err)
	}
}

func TestLaunchAndFindDoesNotSelectWrongApplication(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &applicationFixture{after: []Target{{ID: "wrong", Application: "Calculator", ApplicationID: "/other/Calculator.app"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := LaunchAndFindApplication(ctx, f, app)
	if !errors.Is(err, context.DeadlineExceeded) || len(f.launched) != 1 {
		t.Fatalf("wrong target selected: launched=%+v err=%v", f.launched, err)
	}
}
