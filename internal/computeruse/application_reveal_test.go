package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

type revealApplicationFixture struct {
	applicationFixture
	revealed []Application
	visible  []Target
}

func (f *revealApplicationFixture) Targets(context.Context) ([]Target, error) {
	if len(f.revealed) != 0 {
		return f.visible, nil
	}
	return f.after, nil
}

func (f *revealApplicationFixture) RevealApplication(_ context.Context, app Application) error {
	f.revealed = append(f.revealed, app)
	return nil
}

func TestResolveApplicationRevealsLocalizedExistingApp(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{
		applicationFixture: applicationFixture{apps: []Application{app}},
		visible:            []Target{{ID: "right", Application: "计算器", ApplicationID: app.Identity}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	resolution, err := ResolveApplication(ctx, f, "计算器")
	if err != nil || !resolution.Matched || len(resolution.Targets) != 1 || resolution.Targets[0].ID != "right" || len(f.launched) != 1 || len(f.revealed) != 1 {
		t.Fatalf("localized resolution=%+v launched=%+v revealed=%+v err=%v", resolution, f.launched, f.revealed, err)
	}
}

func TestApplicationRevealFindsOnlyExactVisibleWindows(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{visible: []Target{
		{ID: "wrong", Application: "Calculator", ApplicationID: "/other/Calculator.app"},
		{ID: "right", Application: "计算器", ApplicationID: app.Identity},
		{ID: "also-right", Application: "计算器", ApplicationID: app.Identity},
	}}
	checks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = WithApplicationLaunchGuard(ctx, func() error { checks++; return nil })
	resolution, err := launchAndFindApplication(ctx, f, app, time.Millisecond)
	if err != nil || !resolution.Matched || len(f.launched) != 1 || len(f.revealed) != 1 || f.revealed[0] != app || checks != 2 {
		t.Fatalf("reveal: resolution=%+v launched=%+v revealed=%+v checks=%d err=%v", resolution, f.launched, f.revealed, checks, err)
	}
	if len(resolution.Targets) != 2 || resolution.Targets[0].ID != "right" || resolution.Targets[1].ID != "also-right" {
		t.Fatalf("wrong target or lost ambiguity: %+v", resolution.Targets)
	}
}

func TestApplicationRevealRunsAtMostOnce(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := launchAndFindApplication(ctx, f, app, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || len(f.revealed) != 1 {
		t.Fatalf("reveal must run once: revealed=%+v err=%v", f.revealed, err)
	}
}

func TestApplicationVisibleAfterLaunchDoesNotStealFocus(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{applicationFixture: applicationFixture{after: []Target{{ID: "right", ApplicationID: app.Identity}}}}
	resolution, err := launchAndFindApplication(context.Background(), f, app, time.Millisecond)
	if err != nil || len(resolution.Targets) != 1 || len(f.revealed) != 0 {
		t.Fatalf("visible app should not be revealed: resolution=%+v revealed=%+v err=%v", resolution, f.revealed, err)
	}
}

func TestApplicationRevealRechecksAuthorization(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{}
	denied := errors.New("permission changed")
	checks := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = WithApplicationLaunchGuard(ctx, func() error {
		checks++
		if checks == 2 {
			return denied
		}
		return nil
	})
	_, err := launchAndFindApplication(ctx, f, app, time.Millisecond)
	if !errors.Is(err, denied) || len(f.launched) != 1 || len(f.revealed) != 0 {
		t.Fatalf("reveal ignored permission change: launched=%+v revealed=%+v err=%v", f.launched, f.revealed, err)
	}
}

func TestApplicationRevealHonorsCancellation(t *testing.T) {
	app := Application{ID: "opaque", Name: "Calculator", Identity: "/apps/Calculator.app"}
	f := &revealApplicationFixture{}
	ctx, cancel := context.WithCancel(context.Background())
	checks := 0
	ctx = WithApplicationLaunchGuard(ctx, func() error {
		checks++
		if checks == 2 {
			cancel()
		}
		return nil
	})
	_, err := launchAndFindApplication(ctx, f, app, time.Millisecond)
	if !errors.Is(err, context.Canceled) || len(f.revealed) != 0 {
		t.Fatalf("canceled reveal ran: revealed=%+v err=%v", f.revealed, err)
	}
}
