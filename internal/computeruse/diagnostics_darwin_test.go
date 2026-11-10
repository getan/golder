//go:build darwin && cgo

package computeruse

import (
	"context"
	"errors"
	"testing"
)

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
