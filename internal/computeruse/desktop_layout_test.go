package computeruse

import (
	"errors"
	"testing"
)

func TestDesktopLayoutRequiresLiveForegroundChoice(t *testing.T) {
	var d desktopChoice
	if d.requested(1) || d.finish(1, true) {
		t.Fatal("observation alone must not request or authorize a layout")
	}
	err := d.beforeInput(1)
	if !errors.Is(err, ErrNoInput) || StaleCode(err) != "desktop_layout_choice" || !d.requested(1) {
		t.Fatalf("first foreground input: %v", err)
	}
	if d.requested(2) || d.finish(2, true) {
		t.Fatal("stale choice crossed a session transition")
	}
	_ = d.beforeInput(2)
	if d.finish(1, true) || !d.finish(2, false) || d.beforeInput(2) != nil || d.compact {
		t.Fatal("keep-layout choice was not scoped to the current request")
	}
}
