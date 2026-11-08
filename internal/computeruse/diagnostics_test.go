package computeruse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWindowDiagnosticsAreDistinctAndContainNoWindowContent(t *testing.T) {
	for _, tc := range []struct {
		reason WindowFailure
		want   string
	}{
		{WindowPermissionDenied, "Accessibility permission is not granted"},
		{WindowUnavailable, "could not verify that the selected window remains visible"},
		{WindowBoundsMissing, "bounds"},
		{WindowAXAppUnavailable, "Accessibility reference"},
		{WindowAXDisabled, "Accessibility API is disabled"},
		{WindowAXReadFailed, "AX error -25204"},
		{WindowAXNoWindows, "no Accessibility windows"},
		{WindowAXGeometry, "1 of 2 readable"},
		{WindowAXNoMatch, "none of the 2 Accessibility windows matched"},
		{WindowAXAmbiguous, "2 Accessibility windows matched"},
		{WindowAXLimit, "128-window"},
		{WindowAXFocus, "required focus"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			diag := &WindowDiagnostic{Reason: tc.reason, AXError: -25204, Windows: 2, Readable: 1, Matches: 2}
			if !strings.Contains(diag.Error(), tc.want) || !errors.Is(diag, ErrTargetLost) {
				t.Fatalf("diagnostic = %q", diag)
			}
		})
	}
}

type diagnosticTestBackend struct{ err error }

func (b *diagnosticTestBackend) Validate(context.Context, Target, bool) error { return b.err }
func (*diagnosticTestBackend) Close() error                                   { return nil }

func TestSessionRetainsDiagnosticAndFailsClosed(t *testing.T) {
	diag := &WindowDiagnostic{Reason: WindowAXAmbiguous, Matches: 2}
	backend := &diagnosticTestBackend{err: diag}
	s := New(backend)
	ctx, target := context.Background(), Target{ID: "target"}
	err := s.Start(ctx, target, Capabilities{Observe: true, Control: true})
	var got *WindowDiagnostic
	if !errors.Is(err, ErrTargetLost) || !errors.As(err, &got) || got.Reason != WindowAXAmbiguous || s.Status().Active {
		t.Fatalf("start did not preserve diagnostic or refused state: %v", err)
	}
	backend.err = nil
	if err := s.Start(ctx, target, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	backend.err = diag
	called := false
	err = s.Do(ctx, true, func(context.Context, Backend, Target) error { called = true; return nil })
	if !errors.Is(err, ErrTargetLost) || !errors.As(err, &got) || called || !s.Status().Paused {
		t.Fatalf("unsafe operation or missing diagnostic: err=%v called=%v status=%+v", err, called, s.Status())
	}
	if err := s.Resume(ctx); !errors.As(err, &got) || !s.Status().Paused {
		t.Fatalf("unsafe resume or missing diagnostic: err=%v status=%+v", err, s.Status())
	}
}

func TestPermissionAndFailureDescriptions(t *testing.T) {
	got := DescribeFailure(&WindowDiagnostic{Reason: WindowAXNoMatch, Windows: 1}, PermissionState{ScreenCapture: "granted", Accessibility: "denied"})
	for _, want := range []string{"none of the 1 Accessibility windows matched", "Screen Recording: granted", "Accessibility: denied", "do not authorize a target window"} {
		if !strings.Contains(got, want) {
			t.Fatalf("description missing %q: %s", want, got)
		}
	}
	if got := DescribePermissions(PermissionState{}); !strings.Contains(got, "Screen Recording: unknown; Accessibility: unknown") {
		t.Fatalf("empty permission state = %q", got)
	}
}
