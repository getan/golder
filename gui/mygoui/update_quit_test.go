package mygoui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestGUIUpdateOpenAndQuitRequiresExplicitConfirmation(t *testing.T) {
	a := testApp()
	a.updateTag, a.updatePath, a.updateDialogOpen = "v1.2.3", "/downloads/golder.dmg", true
	a.status.Running = true
	a.draft = "unsent draft"
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Open and quit"); err != nil {
		t.Fatal(err)
	}
	if !a.updateDialogOpen || !a.updateQuitConfirm || a.updateBusy {
		t.Fatal("the initial click must request confirmation without opening or quitting")
	}
	for _, text := range []string{"Open package and quit?", "running tasks in all conversations", "pending approvals", "unsaved drafts or file edits", "not installed automatically", "Back", "Confirm open and quit"} {
		if !tt.HasText(text) {
			t.Fatalf("confirmation missing %q: %q", text, tt.Texts())
		}
	}
	if err := tt.Click("Back"); err != nil || a.updateQuitConfirm || !a.updateDialogOpen || a.updateBusy {
		t.Fatalf("Back did not return to the download prompt: %v", err)
	}
	if !tt.HasText("Open and quit") || a.draft != "unsent draft" {
		t.Fatal("backing out lost the downloaded package or draft")
	}
}

func TestGUIUpdateOpenAndQuitWaitsForVerificationAndSystemOpen(t *testing.T) {
	a := testApp()
	const path = "/downloads/golder.dmg"
	a.updatePath, a.updateDialogOpen, a.updateQuitConfirm = path, true, true
	_, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
	defer cancel()
	var calls []string
	a.finishDownloadedGUIOpen(seq, path, true, nil, func(got string) error {
		if got != path || !a.updateDialogOpen || !a.updateQuitConfirm {
			t.Fatal("package opened without the expected path and active confirmation")
		}
		calls = append(calls, "open")
		return nil
	}, func() {
		if a.updateDialogOpen || a.updateQuitConfirm {
			t.Fatal("normal close must be requested after dismissing the update dialog")
		}
		calls = append(calls, "close")
	})
	if !reflect.DeepEqual(calls, []string{"open", "close"}) || a.updateBusy || a.bannerErr {
		t.Fatalf("wrong open/close sequence: calls=%v busy=%t banner=%q", calls, a.updateBusy, a.banner)
	}
}

func TestGUIUpdateOpenAndQuitFailuresKeepAppRunning(t *testing.T) {
	for _, tc := range []struct {
		name      string
		verifyErr error
		openErr   error
		wantOpen  bool
		keepPath  bool
	}{
		{name: "verification failure", verifyErr: errors.New("digest mismatch")},
		{name: "system open failure", openErr: errors.New("no handler"), wantOpen: true, keepPath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			const path = "/downloads/golder.dmg"
			a.updatePath, a.updateDialogOpen, a.updateQuitConfirm = path, true, true
			_, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
			defer cancel()
			opened, closed := false, false
			a.finishDownloadedGUIOpen(seq, path, true, tc.verifyErr, func(string) error {
				opened = true
				return tc.openErr
			}, func() { closed = true })
			if opened != tc.wantOpen || closed || !a.bannerErr || a.updateBusy || (a.updatePath == path) != tc.keepPath {
				t.Fatalf("failure handling: opened=%t closed=%t path=%q banner=%q", opened, closed, a.updatePath, a.banner)
			}
			if tc.openErr != nil && (!a.updateDialogOpen || !a.updateQuitConfirm || !strings.Contains(a.updateError, "no handler")) {
				t.Fatal("an open failure should remain visible and allow a retry")
			}
		})
	}
}

func TestGUIUpdateQuitCancellationInvalidatesAsyncCompletion(t *testing.T) {
	a := testApp()
	a.updateTag, a.updatePath, a.updateDialogOpen, a.updateQuitConfirm = "v1.2.3", "/downloads/golder.dmg", true, true
	ctx, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
	defer cancel()
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Checking the downloaded GUI package") || !tt.HasText("Cancel opening") {
		t.Fatalf("verification state missing: %q", tt.Texts())
	}
	if err := tt.Click("Cancel opening"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled || a.updateBusy || a.updateQuitConfirm || !a.updateDialogOpen {
		t.Fatal("cancel did not return to the download prompt")
	}
	opened, closed := false, false
	a.finishDownloadedGUIOpen(seq, a.updatePath, true, nil, func(string) error { opened = true; return nil }, func() { closed = true })
	if opened || closed {
		t.Fatal("a late verification result opened or closed after cancellation")
	}
}

func TestGUIUpdateQuitDismissedDialogDoesNotOpenOrQuit(t *testing.T) {
	a := testApp()
	a.updatePath, a.updateDialogOpen, a.updateQuitConfirm = "/downloads/golder.dmg", true, true
	_, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
	defer cancel()
	a.updateDialogOpen = false // Escape or a click outside the modal.
	opened, closed := false, false
	a.finishDownloadedGUIOpen(seq, a.updatePath, true, nil, func(string) error { opened = true; return nil }, func() { closed = true })
	if opened || closed || a.updateBusy || a.updateQuitConfirm || !strings.Contains(a.banner, "cancelled") {
		t.Fatal("dismissing the confirmation must revoke the pending open and quit")
	}
}

func TestGUIUpdateQuitEscapeCancelsVerification(t *testing.T) {
	a := testApp()
	a.updatePath, a.updateDialogOpen, a.updateQuitConfirm = "/downloads/golder.dmg", true, true
	ctx, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
	defer cancel()
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.updateDialogOpen || a.updateQuitConfirm || a.updateBusy || ctx.Err() != context.Canceled {
		t.Fatalf("Escape did not cancel verification: open=%t confirm=%t busy=%t err=%v", a.updateDialogOpen, a.updateQuitConfirm, a.updateBusy, ctx.Err())
	}
	opened, closed := false, false
	a.finishDownloadedGUIOpen(seq, a.updatePath, true, nil, func(string) error { opened = true; return nil }, func() { closed = true })
	if opened || closed {
		t.Fatal("a dismissed update confirmation must not open the package or quit")
	}
}

func TestGUIUpdateOpenWithoutQuitRemainsAvailable(t *testing.T) {
	a := testApp()
	a.updatePath = "/downloads/golder.dmg"
	_, cancel, seq := a.beginGUIUpdateOperation("opening", time.Minute)
	defer cancel()
	opened, closed := false, false
	a.finishDownloadedGUIOpen(seq, a.updatePath, false, nil, func(string) error { opened = true; return nil }, func() { closed = true })
	if !opened || closed || a.updateBusy {
		t.Fatal("the original open-only action must not quit the app")
	}
}
