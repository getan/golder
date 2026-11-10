package mygoui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGUIDownloadProgressAndCompletedActionsAreSeparate(t *testing.T) {
	a := testApp()
	a.updatePackage = guiTestPackage(t, make([]byte, 4<<20), "darwin", "arm64")
	_, cancel, seq := a.beginGUIDownload()
	defer cancel()
	tt := ui.NewTester(a.view, 1200, 800)
	for _, tc := range []struct {
		done int64
		want string
	}{
		{0, "Connecting to download server"},
		{1 << 20, "25% · 1.0 MiB / 4.0 MiB"},
		{4 << 20, "Verifying and saving the package"},
	} {
		a.applyGUIDownloadProgress(seq, tc.done, a.updatePackage.Size)
		tt.Frame()
		if !tt.HasText(tc.want) || !tt.HasText("Cancel download") || !tt.HasText("Continue in background") {
			t.Fatalf("missing visible download state %q: %q", tc.want, tt.Texts())
		}
		if tt.HasText("Open and quit") || tt.HasText("Show in folder") || tt.HasText("GUI v1.2.3 downloaded") {
			t.Fatal("offered installation/exit before the file was verified and saved")
		}
	}
	a.finishGUIDownload(seq, "/downloads/verified.dmg", nil)
	tt.Frame()
	if a.updateBusy || !tt.HasText("Open and quit") || !tt.HasText("GUI v1.2.3 downloaded") || tt.HasText("Cancel download") {
		t.Fatalf("completion did not switch to ready-to-open actions: %q", tt.Texts())
	}
}

func TestGUIDownloadDismissedNotificationAndBackgroundPanelStayDismissed(t *testing.T) {
	a := testApp()
	a.updatePackage = guiTestPackage(t, make([]byte, 1024), "darwin", "arm64")
	_, cancel, seq := a.beginGUIDownload()
	defer cancel()
	a.banner, a.bannerErr = "An earlier download notification", false
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Continue in background"); err != nil || a.updateDialogOpen || !a.updateBusy {
		t.Fatalf("cannot background the download: %v", err)
	}
	if err := tt.Click("Dismiss"); err != nil || a.banner != "" {
		t.Fatalf("cannot dismiss the earlier notification: %v", err)
	}
	for _, n := range []int64{100, 300, 800, 1024} {
		a.applyGUIDownloadProgress(seq, n, 1024)
		if a.banner != "" || a.updateDialogOpen {
			t.Fatal("progress recreated a dismissed banner or reopened the hidden panel")
		}
	}
	a.activateGUIUpdate()
	if !a.updateDialogOpen || !a.updateBusy {
		t.Fatal("Update did not reopen the existing download panel")
	}
}

func TestGUIDownloadCancellationAndFailureNeverOfferOpenOrQuit(t *testing.T) {
	a := testApp()
	a.updatePackage = guiTestPackage(t, []byte("package"), "darwin", "arm64")
	ctx, cancel, seq := a.beginGUIDownload()
	defer cancel()
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Cancel download"); err != nil || ctx.Err() != context.Canceled || a.updateBusy || a.updateDialogOpen {
		t.Fatalf("download cancellation failed: %v ctx=%v", err, ctx.Err())
	}
	a.finishGUIDownload(seq, "/late/file", nil)
	a.applyGUIDownloadProgress(seq, 7, 7)
	if a.updatePath != "" || a.updateDialogOpen || a.updateProgress != 0 {
		t.Fatal("stale completion resurrected a cancelled download")
	}
	_, cancel2, seq2 := a.beginGUIDownload()
	defer cancel2()
	a.finishGUIDownload(seq2, "", errors.New("dial tcp: i/o timeout"))
	tt.Frame()
	if a.updateBusy || !tt.HasText("Download failed:") || !tt.HasText("Retry download") || tt.HasText("Open and quit") || a.updatePath != "" {
		t.Fatalf("failure state is unsafe or invisible: %q", tt.Texts())
	}
	if !strings.Contains(a.updateError, "i/o timeout") {
		t.Fatal("lost failure diagnostics")
	}
}

func TestGUIDownloadProgressBarUsesBoundedFraction(t *testing.T) {
	for _, tc := range []struct {
		percent int
		width   float32
	}{{-1, 0}, {0, 0}, {25, 100}, {100, 400}, {200, 400}} {
		r := guiDownloadProgressRect(ui.Rect{X: 10, Y: 20, W: 400, H: 8}, tc.percent)
		if r.W != tc.width || r.X != 10 || r.Y != 20 || r.H != 8 {
			t.Fatalf("bad progress bar geometry for %d%%: %+v", tc.percent, r)
		}
	}
}
