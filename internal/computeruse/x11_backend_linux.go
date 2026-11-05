//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unsafe"
)

// captureX11Identity associates an already operator-confirmed portal frame
// with exactly one local X11 client, its server-reported PID and geometry.
// Failure leaves the semantic-only capability intact.
func (b *linuxATSPIBackend) captureX11Identity(ctx context.Context, target Target, width, height int) (x11FrameIdentity, error) {
	if err := loadATSPoint(); err != nil {
		return x11FrameIdentity{}, err
	}
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	window, err := b.validate(ctx, target)
	if err != nil {
		return x11FrameIdentity{}, err
	}
	if b.portal == nil || !b.portalApproved || b.portalTarget != target.ID || b.portal.revoked.Load() {
		return x11FrameIdentity{}, ErrTargetLost
	}
	component := atspiComponent(window.object)
	if component == 0 {
		return x11FrameIdentity{}, ErrUnavailable
	}
	defer atspiUnref(component)
	var failure uintptr
	ptr := atspiExtents(component, 0, &failure) // screen coordinates
	if atspiError(failure) != nil || ptr == 0 {
		if ptr != 0 {
			atspiFree(ptr)
		}
		return x11FrameIdentity{}, ErrUnavailable
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	rect := *(*atspiRect)(base)
	atspiFree(ptr)
	client, err := openX11(ctx)
	if err != nil {
		return x11FrameIdentity{}, err
	}
	defer client.close()
	identity, err := client.identify(window)
	if err != nil {
		return x11FrameIdentity{}, err
	}
	if rect.X != identity.Rect.X || rect.Y != identity.Rect.Y || rect.Width != identity.Rect.Width || rect.Height != identity.Rect.Height || int64(width) != int64(rect.Width) || int64(height) != int64(rect.Height) {
		return x11FrameIdentity{}, errors.New("X11 target, accessibility and screenshot geometry do not match")
	}
	if err := ctx.Err(); err != nil {
		return x11FrameIdentity{}, err
	}
	return identity, nil
}

func x11Point(identity x11FrameIdentity, x, y float64) (int32, int32, error) {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < 0 || x >= 1 || y < 0 || y >= 1 || identity.Rect.Width <= 0 || identity.Rect.Height <= 0 {
		return 0, 0, ErrNotAllowed
	}
	px := int64(identity.Rect.X) + int64(math.Floor(x*float64(identity.Rect.Width)))
	py := int64(identity.Rect.Y) + int64(math.Floor(y*float64(identity.Rect.Height)))
	if px < math.MinInt16 || px > math.MaxInt16 || py < math.MinInt16 || py > math.MaxInt16 {
		return 0, 0, ErrNotAllowed
	}
	return int32(px), int32(py), nil
}

func (c *x11Client) verify(ctx context.Context, window atspiWindow, identity x11FrameIdentity, point bool, x, y int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actual, err := c.identify(window)
	if err != nil || actual != identity {
		return ErrTargetLost
	}
	if err := c.focused(identity); err != nil {
		return err
	}
	if err := c.idleInput(); err != nil {
		return err
	}
	if point {
		if err := c.unobscured(identity, x, y); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func x11AccessibleRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if secureATSPIRole(role) {
		return false
	}
	if atspiPointTargetable(role) {
		return true
	}
	switch role {
	case "text", "entry", "paragraph", "document frame", "scroll pane", "viewport", "list", "tree", "table", "table cell", "combo box", "spin button", "slider":
		return true
	default:
		return false
	}
}

func x11AccessibleHit(ctx context.Context, window atspiWindow, component uintptr, x, y int32) (uintptr, error) {
	var failure uintptr
	object := atspiAtPoint(component, x, y, 0, &failure) // screen coordinates
	if atspiError(failure) != nil || object == 0 {
		if object != 0 {
			atspiUnref(object)
		}
		return 0, ErrTargetLost
	}
	role, err := atspiGetString(object, atspiRole)
	if err != nil || !x11AccessibleRole(role) || !atspiTargetContains(ctx, window, object) || !atspiState(object, 8) || !atspiState(object, 24) || !atspiState(object, 25) || !atspiState(object, 30) {
		atspiUnref(object)
		return 0, ErrNotAllowed
	}
	return object, nil
}

// x11Dispatch is used only for a recent frame with an XRes-verified local X11
// identity. It rechecks accessibility, focus, occlusion and held inputs before
// each native request; a failed dispatch is never automatically repeated.
func (b *linuxATSPIBackend) x11Dispatch(ctx context.Context, target Target, binding linuxFrameBinding, action string, x, y float64, dx, dy int, key string) error {
	if !binding.HasX11 || !x11Session() {
		return ErrUnavailable
	}
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	window, err := b.validate(ctx, target)
	if err != nil {
		return err
	}
	if b.portal == nil || !b.portalApproved || b.portalTarget != target.ID || b.portal.revoked.Load() || !b.x11Ready || binding.X11.PID != uint32(window.pid) {
		return ErrTargetLost
	}
	component := atspiComponent(window.object)
	if component == 0 {
		return ErrUnavailable
	}
	defer atspiUnref(component)
	verifyGeometry := func() error {
		var failure uintptr
		ptr := atspiExtents(component, 0, &failure)
		if atspiError(failure) != nil || ptr == 0 {
			if ptr != 0 {
				atspiFree(ptr)
			}
			return ErrTargetLost
		}
		base := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
		rect := *(*atspiRect)(base)
		atspiFree(ptr)
		if rect.X != binding.X11.Rect.X || rect.Y != binding.X11.Rect.Y || rect.Width != binding.X11.Rect.Width || rect.Height != binding.X11.Rect.Height || int64(rect.Width) != int64(binding.Width) || int64(rect.Height) != int64(binding.Height) {
			return ErrStaleObservation
		}
		if _, err := b.validate(ctx, target); err != nil {
			return err
		}
		if b.portal == nil || !b.portalApproved || b.portalTarget != target.ID || b.portal.revoked.Load() {
			return ErrTargetLost
		}
		return ctx.Err()
	}
	if err := verifyGeometry(); err != nil {
		return err
	}
	client, err := openX11(ctx)
	if err != nil {
		return err
	}
	defer client.close()
	if action == "key" {
		focused, err := findATSFocusedWith(ctx, window, x11AccessibleRole)
		if err != nil {
			return err
		}
		defer atspiUnref(focused)
		code, err := client.keycode(key)
		if err != nil {
			return err
		}
		if err := client.verify(ctx, window, binding.X11, false, 0, 0); err != nil {
			return err
		}
		current, err := findATSFocusedWith(ctx, window, x11AccessibleRole)
		if err != nil {
			return err
		}
		same := current == focused
		atspiUnref(current)
		if !same {
			return ErrStaleObservation
		}
		if err := verifyGeometry(); err != nil {
			return err
		}
		if err := client.verify(ctx, window, binding.X11, false, 0, 0); err != nil {
			return err
		}
		if err := client.noActiveGrab(ctx, binding.X11, false); err != nil {
			return err
		}
		if err := client.verify(ctx, window, binding.X11, false, 0, 0); err != nil {
			return err
		}
		current, err = findATSFocusedWith(ctx, window, x11AccessibleRole)
		if err != nil {
			return err
		}
		same = current == focused
		atspiUnref(current)
		if !same {
			return ErrStaleObservation
		}
		freshCode, err := client.keycode(key)
		if err != nil || freshCode != code {
			return ErrStaleObservation
		}
		focusWindow, err := client.focusedWindow(binding.X11)
		if err != nil {
			return err
		}
		if err := client.idleInput(); err != nil {
			return err
		}
		freshFocus, err := client.focusedWindow(binding.X11)
		if err != nil || freshFocus != focusWindow {
			return ErrStaleObservation
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return client.sendPair(ctx, x11EventTarget{Window: focusWindow}, 0, 0, 2, 3, code)
	}
	if action != "click" && action != "scroll" {
		return ErrNotAllowed
	}
	px, py, err := x11Point(binding.X11, x, y)
	if err != nil {
		return err
	}
	if err := client.verify(ctx, window, binding.X11, true, px, py); err != nil {
		return err
	}
	hit, err := x11AccessibleHit(ctx, window, component, px, py)
	if err != nil {
		return err
	}
	defer atspiUnref(hit)
	check := func(sameHit bool) (x11EventTarget, error) {
		if err := verifyGeometry(); err != nil {
			return x11EventTarget{}, err
		}
		if err := client.verify(ctx, window, binding.X11, true, px, py); err != nil {
			return x11EventTarget{}, err
		}
		current, err := x11AccessibleHit(ctx, window, component, px, py)
		if err != nil {
			return x11EventTarget{}, err
		}
		same := current == hit
		atspiUnref(current)
		if sameHit && !same {
			return x11EventTarget{}, ErrStaleObservation
		}
		if err := client.noActiveGrab(ctx, binding.X11, true); err != nil {
			return x11EventTarget{}, err
		}
		if err := client.verify(ctx, window, binding.X11, true, px, py); err != nil {
			return x11EventTarget{}, err
		}
		current, err = x11AccessibleHit(ctx, window, component, px, py)
		if err != nil {
			return x11EventTarget{}, err
		}
		same = current == hit
		atspiUnref(current)
		if sameHit && !same {
			return x11EventTarget{}, ErrStaleObservation
		}
		if err := client.focused(binding.X11); err != nil {
			return x11EventTarget{}, err
		}
		if err := client.unobscured(binding.X11, px, py); err != nil {
			return x11EventTarget{}, err
		}
		destination, err := client.pointTarget(binding.X11, px, py)
		if err != nil {
			return x11EventTarget{}, err
		}
		return destination, ctx.Err()
	}
	if action == "click" {
		destination, err := check(true)
		if err != nil {
			return err
		}
		return client.sendPair(ctx, destination, int16(px), int16(py), 4, 5, 1)
	}
	if dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
		return ErrNotAllowed
	}
	buttons := make([]uint8, 0, 10)
	for n := 0; n < absX11(dx); n++ {
		button := uint8(7) // right
		if dx < 0 {
			button = 6 // left
		}
		buttons = append(buttons, button)
	}
	for n := 0; n < absX11(dy); n++ {
		button := uint8(4) // up
		if dy < 0 {
			button = 5 // down
		}
		buttons = append(buttons, button)
	}
	for i, button := range buttons {
		destination, err := check(i == 0)
		if err != nil {
			if i > 0 {
				return fmt.Errorf("X11 scroll stopped after %d dispatched steps: %w", i, err)
			}
			return err
		}
		if err := client.sendPair(ctx, destination, int16(px), int16(py), 4, 5, button); err != nil {
			return fmt.Errorf("X11 scroll may be incomplete after %d steps: %w", i, err)
		}
	}
	return nil
}

func absX11(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
