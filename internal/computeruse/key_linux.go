//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// On Wayland, a small set of keys has direct target-owned semantic equivalents.
// A verified native X11 frame can additionally support the bounded key set.
func (b *linuxATSPIBackend) SupportsFrameKey(key string) bool {
	if key == "enter" || key == "space" || key == "escape" {
		return true
	}
	if !validFrameKey(key) || !x11Session() {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && b.portalApproved && b.x11Ready
}

func atspiKeyTargetable(role, key string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	switch key {
	case "enter":
		return role == "push button" || role == "link" || role == "menu item"
	case "space":
		return role == "push button" || role == "toggle button" || role == "check box" || role == "radio button"
	default:
		return false
	}
}

func findATSFocused(ctx context.Context, window atspiWindow, key string) (uintptr, error) {
	return findATSFocusedWith(ctx, window, func(role string) bool { return atspiKeyTargetable(role, key) })
}

func findATSFocusedWith(ctx context.Context, window atspiWindow, allowed func(string) bool) (uintptr, error) {
	if !atspiState(window.object, 1) { // ATSPI_STATE_ACTIVE
		return 0, ErrTargetLost
	}
	budget := 500
	seen := make(map[uintptr]bool)
	var match uintptr
	unsafeFocus, incomplete := false, false
	var visit func(uintptr, int, bool)
	visit = func(node uintptr, depth int, ancestorSecure bool) {
		if ctx.Err() != nil || budget <= 0 || depth > 16 || seen[node] {
			incomplete = true
			return
		}
		seen[node] = true
		budget--
		pid, pidErr := atspiGetPID(node)
		role, roleErr := atspiGetString(node, atspiRole)
		if pidErr != nil || roleErr != nil || pid != window.pid {
			incomplete = true
			return
		}
		state := atspiStates(node)
		if state == 0 {
			incomplete = true
			return
		}
		secure := ancestorSecure || secureATSPIRole(role)
		focused := atspiContains(state, 12) != 0
		defunct := atspiContains(state, 6) != 0
		enabled := atspiContains(state, 8) != 0 && atspiContains(state, 24) != 0 && atspiContains(state, 25) != 0 && atspiContains(state, 30) != 0
		atspiUnref(state)
		if defunct {
			incomplete = true
			return
		}
		if focused {
			if secure || !enabled || !allowed(role) || match != 0 {
				unsafeFocus = true
			} else {
				match = atspiRef(node)
				if match == 0 {
					incomplete = true
				}
			}
		}
		var failure uintptr
		count := int(atspiCount(node, &failure))
		if atspiError(failure) != nil || count < 0 || count > 256 {
			incomplete = true
			return
		}
		for i := 0; i < count; i++ {
			child, err := atspiGetChild(node, i)
			if err != nil {
				incomplete = true
				continue
			}
			visit(child, depth+1, secure)
			atspiUnref(child)
		}
	}
	visit(window.object, 0, false)
	if unsafeFocus || incomplete || match == 0 || ctx.Err() != nil {
		if match != 0 {
			atspiUnref(match)
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return 0, ErrUnavailable
	}
	return match, nil
}

func (b *linuxATSPIBackend) PressFrame(ctx context.Context, target Target, token, key string) error {
	if !b.SupportsFrameKey(key) {
		return ErrNotAllowed
	}
	var previous linuxFrameBinding
	if json.Unmarshal([]byte(token), &previous) != nil || previous.Target != target.ID {
		return ErrStaleObservation
	}
	_, fresh, err := b.CaptureBound(ctx, target)
	if err != nil {
		return err
	}
	var current linuxFrameBinding
	if json.Unmarshal([]byte(fresh), &current) != nil || current != previous {
		return ErrStaleObservation
	}
	if key != "enter" && key != "space" && key != "escape" {
		if previous.HasX11 {
			return b.x11Dispatch(ctx, target, previous, "key", 0, 0, 0, 0, key)
		}
		return ErrUnavailable
	}
	if key == "escape" {
		err = b.pressEscapeSemanticFrame(ctx, target)
	} else {
		err = b.pressSemanticFrame(ctx, target, key)
	}
	if previous.HasX11 && errors.Is(err, ErrUnavailable) && !errors.Is(err, errLinuxSemanticAttempted) {
		return b.x11Dispatch(ctx, target, previous, "key", 0, 0, 0, 0, key)
	}
	return err
}

func (b *linuxATSPIBackend) pressSemanticFrame(ctx context.Context, target Target, key string) error {
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	window, err := b.validate(ctx, target)
	if err != nil {
		return err
	}
	if b.portal == nil || !b.portalApproved || b.portalTarget != target.ID || b.portal.revoked.Load() {
		return ErrTargetLost
	}
	object, err := findATSFocused(ctx, window, key)
	if err != nil {
		return err
	}
	defer atspiUnref(object)
	role, err := atspiGetString(object, atspiRole)
	if err != nil || !atspiKeyTargetable(role, key) {
		return ErrNotAllowed
	}
	name, err := atspiGetString(object, atspiName)
	if err != nil {
		return ErrUnavailable
	}
	action := atspiAction(object)
	if action == 0 {
		return ErrUnavailable
	}
	defer atspiUnref(action)
	var failure uintptr
	count := int(atspiActionCount(action, &failure))
	if atspiError(failure) != nil || count < 1 || count > 32 {
		return ErrUnavailable
	}
	index := -1
	for i := 0; i < count; i++ {
		failure = 0
		actionName := atspiString(atspiActionName(action, int32(i), &failure))
		if atspiError(failure) != nil {
			return ErrUnavailable
		}
		switch strings.ToLower(actionName) {
		case "click", "press", "activate":
			if index >= 0 {
				return ErrUnavailable
			}
			index = i
		}
	}
	if index < 0 {
		return ErrUnavailable
	}
	currentFocus, err := findATSFocused(ctx, window, key)
	if err != nil {
		return err
	}
	same := currentFocus == object
	atspiUnref(currentFocus)
	if !same {
		return ErrStaleObservation
	}
	currentRole, roleErr := atspiGetString(object, atspiRole)
	if roleErr != nil || currentRole != role || !atspiKeyTargetable(currentRole, key) {
		return ErrStaleObservation
	}
	currentName, nameErr := atspiGetString(object, atspiName)
	if nameErr != nil || currentName != name {
		return ErrStaleObservation
	}
	if _, err := b.validate(ctx, target); err != nil {
		return err
	}
	if b.portal == nil || !b.portalApproved || b.portalTarget != target.ID || b.portal.revoked.Load() {
		return ErrTargetLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	failure = 0
	ok := atspiDoAction(action, int32(index), &failure) != 0
	if atspiError(failure) != nil || !ok {
		return fmt.Errorf("%w: Linux focused action was refused", errLinuxSemanticAttempted)
	}
	return nil
}
