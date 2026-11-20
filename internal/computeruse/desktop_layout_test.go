package computeruse

import (
	"errors"
	"testing"
	"time"
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

func TestDesktopLayoutExpiresToKeep(t *testing.T) {
	var d desktopChoice
	if err := d.beforeInput(7); err == nil {
		t.Fatal("first foreground input did not request a layout")
	}
	d.mu.Lock()
	d.deadline = time.Now().Add(-time.Second)
	d.mu.Unlock()
	if !d.expire(7) || !d.allowed || d.compact || !d.expired {
		t.Fatal("expired choice did not degrade to keeping the layout")
	}
	if err := d.beforeInput(7); err != nil {
		t.Fatalf("expired choice still blocked input: %v", err)
	}
}
