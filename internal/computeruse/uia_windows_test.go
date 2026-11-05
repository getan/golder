//go:build windows

package computeruse

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestWindowsProtectedExecutables(t *testing.T) {
	for _, path := range []string{`C:\golder.exe`, `C:\Windows\System32\cmd.exe`, `C:\Windows\System32\consent.exe`} {
		if !protectedWindowsExecutable(path) {
			t.Errorf("executable %q should be protected", path)
		}
	}
	if protectedWindowsExecutable(`C:\Apps\Calculator.exe`) {
		t.Fatal("ordinary app should not be protected")
	}
}

func TestWindowsRejectsInvalidActionsBeforeNativeCalls(t *testing.T) {
	b := &windowsBackend{}
	for _, test := range []struct{ action, text string }{
		{"unknown", ""}, {"click", "unexpected"}, {"fill", "bad\x00text"},
	} {
		if err := b.Act(context.Background(), Target{}, "", "", test.action, test.text); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("action %q text %q: %v", test.action, test.text, err)
		}
	}
}

func TestWindowsHelperOutputIsBounded(t *testing.T) {
	var output boundedWindowsOutput
	if _, err := output.Write([]byte(strings.Repeat("x", 12<<20))); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("oversized output was accepted")
	}
	var diagnostic boundedWindowsDiagnostic
	if n, err := diagnostic.Write([]byte(strings.Repeat("x", 8192))); err != nil || n != 8192 || diagnostic.buffer.Len() != 4096 {
		t.Fatalf("unbounded diagnostic: bytes=%d error=%v stored=%d", n, err, diagnostic.buffer.Len())
	}
}

func TestWindowsRejectsInvalidFrameActions(t *testing.T) {
	b := &windowsBackend{}
	ctx := context.Background()
	if err := b.ClickFrame(ctx, Target{}, "", math.NaN(), 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("NaN click: %v", err)
	}
	if err := b.ScrollFrame(ctx, Target{}, "", 0, 0, 0, 6); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("oversized scroll: %v", err)
	}
	if err := b.PressFrame(ctx, Target{}, "", "control"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("modifier key: %v", err)
	}
}
