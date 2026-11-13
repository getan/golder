package mygoui

import (
	"context"
	"testing"
)

func TestGUIUpdateMonitorStopIsIdempotent(t *testing.T) {
	a := testApp()
	ctx, cancel := context.WithCancel(context.Background())
	a.updateMonitorCancel = cancel
	a.stopGUIUpdateMonitor()
	a.stopGUIUpdateMonitor()
	if ctx.Err() != context.Canceled || a.updateMonitorCancel != nil {
		t.Fatal("monitor cancellation was not cleared")
	}
}

func TestGUIUpdateChecksDoNotOverlapOrReplaceStagedDownload(t *testing.T) {
	a := testApp()
	if !a.guiUpdateCheckAllowed(false) {
		t.Fatal("idle app should permit a background check")
	}
	for _, phase := range []string{"checking", "downloading", "opening"} {
		a.updateBusy, a.updatePhase = true, phase
		if a.guiUpdateCheckAllowed(false) || a.guiUpdateCheckAllowed(true) {
			t.Fatalf("check overlapped active %s operation", phase)
		}
	}
	a.updateBusy = false
	a.updatePath = "/downloads/golder.dmg"
	if a.guiUpdateCheckAllowed(false) || !a.guiUpdateCheckAllowed(true) {
		t.Fatal("background check should preserve a downloaded installer")
	}
	a.updatePath, a.updateDialogOpen = "", true
	if a.guiUpdateCheckAllowed(false) {
		t.Fatal("background check should not replace an open update dialog")
	}
}
