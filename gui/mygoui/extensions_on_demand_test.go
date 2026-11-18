package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLarkOnDemandStateExplainsMultiSessionReadiness(t *testing.T) {
	a := testApp()
	a.extension.Installed, a.extension.Enabled, a.extension.Trusted = true, true, true
	a.extension.State = "on demand"
	tt := ui.NewTester(a.larkExtensionCard, 1000, 900)
	if !tt.HasText("on demand") || !tt.HasText("Other idle conversations do not hold the shared login state lock") {
		t.Fatalf("on-demand source was presented as disconnected: %q", tt.Texts())
	}
}

// The card's status is current readiness. An operation holding the shared lock
// right now is shown beside it, and an older failure is shown as history.
func TestLarkCardShowsLiveLockAndKeepsHistorySeparate(t *testing.T) {
	a := testApp()
	a.extension.Installed, a.extension.Enabled, a.extension.Trusted = true, true, true
	a.extension.State = "on demand"
	a.extension.Busy = true
	a.extension.LastAttempt = "2026-11-18 14:32 · call: boom"
	tt := ui.NewTester(a.larkExtensionCard, 1000, 900)
	if !tt.HasText("on demand · in use now") {
		t.Fatalf("live lock was not shown beside the state: %q", tt.Texts())
	}
	if !tt.HasText("In use now: an operation in this or another session holds the shared login state lock") {
		t.Fatalf("busy explanation missing: %q", tt.Texts())
	}
	if !tt.HasText("Last attempt failed: 2026-11-18 14:32 · call: boom") {
		t.Fatalf("history missing: %q", tt.Texts())
	}
}
