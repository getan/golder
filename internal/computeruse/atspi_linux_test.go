//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func TestLinuxATSPIProtectedRolesAndExecutables(t *testing.T) {
	for _, role := range []string{"", "unknown", "invalid", "password text", "Protected entry"} {
		if !secureATSPIRole(role) {
			t.Errorf("role %q should be protected", role)
		}
	}
	if secureATSPIRole("push button") {
		t.Fatal("ordinary button treated as protected")
	}
	for _, path := range []string{"/usr/bin/golder", "/usr/bin/gnome-terminal-server", "/usr/bin/kitty", "/usr/bin/polkit-agent"} {
		if !protectedLinuxExecutable(path) {
			t.Errorf("executable %q should be protected", path)
		}
	}
	if _, _, err := linuxProcessIdentity(os.Getpid()); !errors.Is(err, ErrTargetLost) {
		t.Fatalf("own process should be rejected: %v", err)
	}
}

func TestLinuxATSPIRejectsInvalidActionsBeforeNativeCalls(t *testing.T) {
	b := &linuxATSPIBackend{}
	for _, test := range []struct{ action, text string }{
		{"unknown", ""}, {"click", "unexpected"}, {"fill", "bad\x00text"},
	} {
		if err := b.Act(context.Background(), Target{}, "", "", test.action, test.text); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("action %q text %q: %v", test.action, test.text, err)
		}
	}
}

func TestPortalObjectPathIsStrict(t *testing.T) {
	for _, path := range []string{"", "/org/freedesktop/portal/desktop/session/evil'", "/other/session/one"} {
		if portalObjectPath(path) {
			t.Fatalf("accepted unsafe session path %q", path)
		}
	}
	if !portalObjectPath("/org/freedesktop/portal/desktop/session/1_42/golder123") {
		t.Fatal("rejected valid session path")
	}
}

func TestLinuxFramePointRestrictions(t *testing.T) {
	for _, role := range []string{"", "unknown", "frame", "canvas", "password text", "unrecognized role"} {
		if atspiPointTargetable(role) {
			t.Fatalf("role %q must not be clicked by point", role)
		}
	}
	if !atspiPointTargetable("push button") {
		t.Fatal("push button should be eligible for a scoped semantic action")
	}
	if !atspiKeyTargetable("link", "enter") || atspiKeyTargetable("link", "space") || atspiKeyTargetable("check box", "enter") || !atspiKeyTargetable("check box", "space") {
		t.Fatal("semantic keys must not turn a different key action into a click")
	}
	b := &linuxATSPIBackend{}
	if err := b.ClickFrame(context.Background(), Target{}, "", math.NaN(), 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("invalid point: %v", err)
	}
	if err := b.ScrollFrame(context.Background(), Target{}, "", 0, 0, 6, 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("invalid scroll: %v", err)
	}
	if !b.SupportsFrameKey("enter") || !b.SupportsFrameKey("space") || !b.SupportsFrameKey("escape") || b.SupportsFrameKey("tab") {
		t.Fatal("Linux key capabilities must reflect scoped semantic support")
	}
	if !atspiEscapeContainer("dialog") || !atspiEscapeContainer("popup menu") || atspiEscapeContainer("frame") || atspiEscapeContainer("password text") {
		t.Fatal("Escape must not close an unrelated top-level window")
	}
	if err := b.PressFrame(context.Background(), Target{}, "", "tab"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unsupported Linux key: %v", err)
	}
}

func TestX11RequiresLocalNativeSession(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	if !x11Session() {
		t.Fatal("local native X11 session was not recognized")
	}
	backend := &linuxATSPIBackend{portalApproved: true, x11Ready: true}
	if !backend.SupportsFrameKey("tab") || backend.SupportsFrameKey("control") {
		t.Fatal("verified X11 must expose only the bounded key set")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if x11Session() {
		t.Fatal("Xwayland must not be treated as a complete X11 desktop")
	}
	if backend.SupportsFrameKey("tab") {
		t.Fatal("Xwayland must not expose global keyboard input")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "remote.example:0")
	if x11Session() {
		t.Fatal("remote X11 must not be treated as a local desktop")
	}
}

func TestX11PointAndKeyRestrictions(t *testing.T) {
	identity := x11FrameIdentity{Rect: x11Rect{X: -100, Y: 50, Width: 200, Height: 100}}
	x, y, err := x11Point(identity, .5, .5)
	if err != nil || x != 0 || y != 100 {
		t.Fatalf("point = %d,%d (%v)", x, y, err)
	}
	if _, _, err := x11Point(identity, 1, 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("out-of-bounds point: %v", err)
	}
	if _, ok := x11KeySym("control"); ok {
		t.Fatal("modifier key was accepted")
	}
	if !x11AccessibleRole("entry") || x11AccessibleRole("password text") || x11AccessibleRole("canvas") {
		t.Fatal("X11 accessibility role safety policy is incorrect")
	}
}
