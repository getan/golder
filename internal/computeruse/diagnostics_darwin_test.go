//go:build darwin && cgo

package computeruse

import (
	"context"
	"errors"
	"testing"
)

func TestMacActivationSkipsOnlyNonWindowRecords(t *testing.T) {
	for _, tc := range []struct {
		peer              uint32
		layer, resolvable int
		want              bool
	}{
		{1, 0, 1, false}, // selected window
		{2, 0, 0, false}, // unresolvable menu/decorative record
		{2, 1, 1, false},
		{2, 0, 1, true}, // real peer still prevents an unsafe Space change
	} {
		if got := macActivationPeerRelevant(1, tc.peer, tc.layer, tc.resolvable); got != tc.want {
			t.Errorf("peer %+v: relevant=%t", tc, got)
		}
	}
}

func TestMacFrameRefusalBeforeDispatchHasMarker(t *testing.T) {
	c := NewMacWindowCatalog()
	defer c.Close()
	err := c.ClickFrame(context.Background(), Target{ID: "missing"}, "invalid token", 0, 0)
	if !errors.Is(err, ErrNoInput) || !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("invalid binding lost no-input marker: %v", err)
	}
}

func TestMacAXFallbackRequiresAgreementAndEmptyList(t *testing.T) {
	for _, tc := range []struct {
		windows, main, focused, same int
		want                         bool
	}{
		{0, 1, 1, 1, true},
		{1, 1, 1, 1, false}, // must not bypass an existing window list
		{0, 0, 1, 1, false},
		{0, 1, 0, 1, false},
		{0, 1, 1, 0, false}, // different main/focused windows
	} {
		if got := macAXFallbackAllowed(tc.windows, tc.main, tc.focused, tc.same); got != tc.want {
			t.Errorf("fallback %+v = %t", tc, got)
		}
	}
}

func TestMacAXFallbackRequiresUniqueCGWindow(t *testing.T) {
	for scenario, tc := range []struct {
		name string
		want bool
	}{
		{"unique selected window", true},
		{"identical bounds in same process", false},
		{"identical bounds in another process", true},
		{"identical bounds on non-window layer", true},
		{"unreadable peer window", false},
		{"selected ID missing", false},
		{"canceled", false},
		{"selected geometry outside tolerance", false},
		{"competing geometry within tolerance", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := macCGUniqueBoundsFixture(scenario); got != tc.want {
				t.Errorf("unique = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestMacAXWindowListResultsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		errorCode, hasValue, isArray int
		want                         WindowFailure
	}{
		{0, 1, 1, ""},
		{0, 0, 0, WindowAXNullValue},
		{0, 1, 0, WindowAXUnexpectedType},
		{-25212, 0, 0, WindowAXNoValue},    // kAXErrorNoValue
		{-25211, 0, 0, WindowAXDisabled},   // kAXErrorAPIDisabled
		{-25204, 0, 0, WindowAXReadFailed}, // kAXErrorCannotComplete
	} {
		if got := macAXListReason(tc.errorCode, tc.hasValue, tc.isArray); got != tc.want {
			t.Errorf("error=%d value=%d array=%d: got %q want %q", tc.errorCode, tc.hasValue, tc.isArray, got, tc.want)
		}
	}
}

func TestMacAXMatchingDiagnosticFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		windows, readable, matches int
		want                       WindowFailure
	}{
		{0, 0, 0, WindowAXNoWindows},
		{1, 0, 0, WindowAXGeometry},
		{2, 1, 1, WindowAXGeometry}, // an unreadable second window could also match
		{2, 2, 0, WindowAXNoMatch},
		{2, 2, 2, WindowAXAmbiguous},
		{129, 128, 1, WindowAXLimit},
		{2, 2, 1, ""}, // the sole safely verified match
	} {
		if got := macAXMatchReason(tc.windows, tc.readable, tc.matches); got != tc.want {
			t.Errorf("windows=%d readable=%d matches=%d: got %q want %q", tc.windows, tc.readable, tc.matches, got, tc.want)
		}
	}
}

func TestMacMissingSelectedWindowHasDiagnostic(t *testing.T) {
	c := NewMacWindowCatalog()
	defer c.Close()
	err := c.Validate(context.Background(), Target{ID: "missing"}, false)
	var diagnostic *WindowDiagnostic
	if !errors.As(err, &diagnostic) || diagnostic.Reason != WindowUnavailable || !errors.Is(err, ErrTargetLost) {
		t.Fatalf("missing selected window = %v", err)
	}
}

func TestMacFocusCannotActivateUnknownOrCanceledTarget(t *testing.T) {
	c := NewMacWindowCatalog()
	defer c.Close()
	if focused, err := c.selectedFocus(context.Background(), Target{ID: "unknown"}); focused || !errors.Is(err, ErrTargetLost) {
		t.Fatalf("unknown target focus check: focused=%t err=%v", focused, err)
	}
	if err := c.focusSelected(context.Background(), Target{ID: "unknown"}); !errors.Is(err, ErrTargetLost) {
		t.Fatalf("unknown target activation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.focusSelected(ctx, Target{ID: "unknown"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled target activation: %v", err)
	}
}
