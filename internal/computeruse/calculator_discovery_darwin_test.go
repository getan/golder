//go:build darwin && cgo

package computeruse

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Read-only opt-in coverage of the real window-discovery and identity path.
// Unlike TestMacCalculatorLive, this never launches, activates, or sends input.
func TestMacCalculatorDiscoveryLive(t *testing.T) {
	if os.Getenv("GOLDER_COMPUTERUSE_DISCOVERY_TEST") != "1" {
		t.Skip("set GOLDER_COMPUTERUSE_DISCOVERY_TEST=1 for read-only Calculator discovery")
	}
	if p := ProbePermissions(); p.Accessibility != "granted" || p.ScreenCapture != "granted" {
		t.Skipf("read-only discovery requires existing macOS permissions: %+v", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	backend := NewMacWindowCatalog()
	defer backend.Close()
	ctx = WithApplicationLaunchGuard(ctx, func() error { return errors.New("read-only discovery must not launch Calculator") })
	resolution, err := ResolveApplication(ctx, backend, "Calculator")
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Matched || len(resolution.Targets) != 1 {
		t.Fatalf("expected one real Calculator window, found %d (matched=%t)", len(resolution.Targets), resolution.Matched)
	}
	for _, target := range resolution.Targets {
		err := backend.Validate(ctx, target, false)
		t.Logf("%s validation: %v", target.Detail, err)
		if err != nil {
			t.Fatal(err)
		}
		// Observe twice to also exercise the pinned-AX-identity check. Do not
		// log UI contents, capture pixels, or perform a control action.
		for range 2 {
			_, elements, truncated, err := backend.Snapshot(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			if len(elements) == 0 {
				t.Fatal("Calculator AX snapshot is empty")
			}
			t.Logf("AX snapshot elements=%d truncated=%t", len(elements), truncated)
		}
	}
}
