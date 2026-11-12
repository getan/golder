package computeruse

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTransientWindowValidationClassifiesOnlyReadinessFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&WindowDiagnostic{Reason: WindowAXNoMatch}, true},
		{&WindowDiagnostic{Reason: WindowAXGeometry}, true},
		{&WindowDiagnostic{Reason: WindowAXNoWindows}, true},
		{&WindowDiagnostic{Reason: WindowAXNoValue}, true},
		{&WindowDiagnostic{Reason: WindowAXNullValue}, true},
		{&WindowDiagnostic{Reason: WindowAXUnexpectedType}, false},
		{&WindowDiagnostic{Reason: WindowBoundsMissing}, true},
		{&WindowDiagnostic{Reason: WindowAXReadFailed, AXError: -25204}, true},
		{&WindowDiagnostic{Reason: WindowAXReadFailed, AXError: -25211}, false},
		{&WindowDiagnostic{Reason: WindowAXAmbiguous}, false},
		{&WindowDiagnostic{Reason: WindowPermissionDenied}, false},
		{&WindowDiagnostic{Reason: WindowAXDisabled}, false},
		{&WindowDiagnostic{Reason: WindowUnavailable}, false},
		{ErrTargetLost, false},
		{ErrPaused, false},
		{nil, false},
	} {
		if got := transientWindowValidation(tc.err); got != tc.want {
			t.Errorf("transient(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}

func TestWindowValidationWaitsForSameRestoringWindow(t *testing.T) {
	checks := 0
	err := waitForWindowValidation(context.Background(), time.Second, time.Millisecond, func() error {
		checks++
		if checks < 3 {
			return fmt.Errorf("selected window: %w", &WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1})
		}
		return nil
	})
	if err != nil || checks != 3 {
		t.Fatalf("settling window: checks=%d err=%v", checks, err)
	}
}

func TestWindowValidationNeverRetriesAmbiguousOrChangedTargets(t *testing.T) {
	for _, err := range []error{
		&WindowDiagnostic{Reason: WindowAXAmbiguous, Matches: 2},
		&WindowDiagnostic{Reason: WindowUnavailable},
		&WindowDiagnostic{Reason: WindowPermissionDenied},
		ErrTargetLost,
	} {
		checks := 0
		got := waitForWindowValidation(context.Background(), time.Second, time.Millisecond, func() error {
			checks++
			return err
		})
		if checks != 1 || got != err {
			t.Errorf("unsafe failure retried: checks=%d got=%v want=%v", checks, got, err)
		}
	}
}

func TestWindowValidationStopsWhenTargetBecomesAmbiguous(t *testing.T) {
	checks := 0
	ambiguous := &WindowDiagnostic{Reason: WindowAXAmbiguous, Matches: 2}
	err := waitForWindowValidation(context.Background(), time.Second, time.Millisecond, func() error {
		checks++
		if checks == 1 {
			return &WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1}
		}
		return ambiguous
	})
	if err != ambiguous || checks != 2 {
		t.Fatalf("ambiguity should immediately stop retries: checks=%d err=%v", checks, err)
	}
}

func TestWindowValidationReturnsPersistentDiagnosticAndHonorsCancel(t *testing.T) {
	diag := &WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1}
	checks := 0
	err := waitForWindowValidation(context.Background(), 20*time.Millisecond, time.Millisecond, func() error {
		checks++
		return diag
	})
	if checks < 1 || err != diag || !errors.Is(err, ErrTargetLost) {
		t.Fatalf("persistent mismatch must remain a failure: checks=%d err=%v", checks, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	checks = 0
	err = waitForWindowValidation(ctx, time.Second, time.Millisecond, func() error {
		checks++
		cancel()
		return diag
	})
	if !errors.Is(err, context.Canceled) || checks != 1 {
		t.Fatalf("canceled validation kept retrying: checks=%d err=%v", checks, err)
	}
}

func TestWindowValidationCountsInitialReadAgainstRetryBudget(t *testing.T) {
	diag := &WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1}
	checks := 0
	err := waitForWindowValidation(context.Background(), time.Millisecond, time.Millisecond, func() error {
		checks++
		time.Sleep(5 * time.Millisecond)
		return diag
	})
	if err != diag || checks != 1 {
		t.Fatalf("expired budget retried a slow read: checks=%d err=%v", checks, err)
	}
}

type settlingApplicationFixture struct {
	revealApplicationFixture
	checks int
	target Target
}

func (f *settlingApplicationFixture) Validate(ctx context.Context, target Target, _ bool) error {
	if target != f.target {
		return ErrTargetLost
	}
	return waitForWindowValidation(ctx, time.Second, time.Millisecond, func() error {
		f.checks++
		if f.checks < 3 {
			return &WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1}
		}
		return nil
	})
}

func TestLaunchedOrRestoredApplicationWaitsForSelectedWindow(t *testing.T) {
	app := Application{ID: "app", Name: "Calculator", Identity: "/apps/Calculator.app"}
	target := Target{ID: "window", Application: "Calculator", ApplicationID: app.Identity}
	for _, restored := range []bool{false, true} {
		name := "new launch"
		if restored {
			name = "restored minimized app"
		}
		t.Run(name, func(t *testing.T) {
			f := &settlingApplicationFixture{target: target}
			if restored {
				f.visible = []Target{target}
			} else {
				f.after = []Target{target}
			}
			resolution, err := launchAndFindApplication(context.Background(), f, app, time.Millisecond)
			if err != nil || !resolution.Matched || len(resolution.Targets) != 1 || resolution.Targets[0] != target {
				t.Fatalf("resolution=%+v err=%v", resolution, err)
			}
			s := New(f)
			if err := s.Start(context.Background(), resolution.Targets[0], Capabilities{Observe: true, Control: true}); err != nil {
				t.Fatal(err)
			}
			if !s.Status().Active || s.Status().Target != target || f.checks != 3 || len(f.launched) != 1 || (len(f.revealed) == 1) != restored {
				t.Fatalf("wrong target or repeated launch: status=%+v checks=%d launched=%d revealed=%d", s.Status(), f.checks, len(f.launched), len(f.revealed))
			}
		})
	}
}
