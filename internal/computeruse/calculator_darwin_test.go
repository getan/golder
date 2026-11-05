//go:build darwin && cgo

package computeruse

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// This opt-in test uses the operator's running Calculator. It never requests
// macOS grants, and must only be run after the operator explicitly chose the
// app and granted Screen Recording and Accessibility to the test process.
func TestMacCalculatorLive(t *testing.T) {
	if os.Getenv("GOLDER_COMPUTERUSE_CALCULATOR_TEST") != "1" {
		t.Skip("set GOLDER_COMPUTERUSE_CALCULATOR_TEST=1 for the operator-approved Calculator test")
	}
	permission := ProbePermissions()
	if permission.Accessibility != "granted" || permission.ScreenCapture != "granted" {
		t.Skipf("Calculator live test requires macOS Accessibility and Screen Recording for this process (accessibility=%s, screen=%s)", permission.Accessibility, permission.ScreenCapture)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	backend := NewMacWindowCatalog()
	session := New(backend)
	defer session.Close()
	targets, err := backend.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var matches []Target
	for _, target := range targets {
		if strings.EqualFold(target.Application, "Calculator") || target.Application == "计算器" {
			matches = append(matches, target)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one visible Calculator window, found %d", len(matches))
	}
	if err := session.Start(ctx, matches[0], Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	frame, err := session.Capture(ctx)
	if err != nil || frame.Width < 1 || frame.Height < 1 {
		t.Fatalf("Calculator capture failed: %v", err)
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := ""
	for _, element := range snapshot.Elements {
		if element.Name == "2" && element.Enabled {
			ref = element.Ref
			break
		}
	}
	if ref == "" {
		t.Fatalf("Calculator button 2 not found in AX snapshot (elements=%d truncated=%t)", len(snapshot.Elements), snapshot.Truncated)
	}
	if err := session.Act(ctx, snapshot.ID, ref, "click", ""); err != nil {
		t.Fatalf("Calculator AX press failed: %v", err)
	}
}
