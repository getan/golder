//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"fmt"
	"strings"
)

func atspiEscapeContainer(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "dialog", "alert", "menu", "popup menu", "combo box":
		return true
	default:
		return false
	}
}

// Only an explicit cancel/dismiss action on the nearest containing popup or
// dialog can stand in for Escape. Never skip an inner popup to close its parent.
func findATSEscapeAction(ctx context.Context, window atspiWindow, focus uintptr) (uintptr, uintptr, int32, string, error) {
	current := atspiRef(focus)
	if current == 0 {
		return 0, 0, 0, "", ErrTargetLost
	}
	defer func() {
		if current != 0 {
			atspiUnref(current)
		}
	}()
	seen := make(map[uintptr]bool)
	for depth := 0; depth < 16 && current != 0 && !seen[current]; depth++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, "", err
		}
		seen[current] = true
		if !atspiTargetContains(ctx, window, current) {
			return 0, 0, 0, "", ErrTargetLost
		}
		role, err := atspiGetString(current, atspiRole)
		if err != nil || secureATSPIRole(role) {
			return 0, 0, 0, "", ErrNotAllowed
		}
		if atspiEscapeContainer(role) {
			if !atspiState(current, 8) || !atspiState(current, 24) || !atspiState(current, 25) || !atspiState(current, 30) {
				return 0, 0, 0, "", ErrTargetLost
			}
			action := atspiAction(current)
			if action == 0 {
				return 0, 0, 0, "", ErrUnavailable
			}
			var failure uintptr
			count := int(atspiActionCount(action, &failure))
			if atspiError(failure) != nil || count < 1 || count > 32 {
				atspiUnref(action)
				return 0, 0, 0, "", ErrUnavailable
			}
			chosen, chosenName := int32(-1), ""
			for i := 0; i < count; i++ {
				failure = 0
				name := strings.ToLower(strings.TrimSpace(atspiString(atspiActionName(action, int32(i), &failure))))
				if atspiError(failure) != nil {
					atspiUnref(action)
					return 0, 0, 0, "", ErrUnavailable
				}
				if name == "cancel" || name == "dismiss" {
					if chosen >= 0 {
						atspiUnref(action)
						return 0, 0, 0, "", ErrUnavailable
					}
					chosen, chosenName = int32(i), name
				}
			}
			if chosen < 0 {
				atspiUnref(action)
				return 0, 0, 0, "", ErrUnavailable
			}
			candidate := current
			current = 0
			return candidate, action, chosen, chosenName, nil
		}
		if current == window.object {
			break
		}
		parent, err := atspiScrollParent(current)
		if err != nil {
			return 0, 0, 0, "", err
		}
		atspiUnref(current)
		current = parent
	}
	return 0, 0, 0, "", ErrUnavailable
}

func (b *linuxATSPIBackend) pressEscapeSemanticFrame(ctx context.Context, target Target) error {
	if err := loadATSScroll(); err != nil {
		return err
	}
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
	focus, err := findATSFocusedWith(ctx, window, x11AccessibleRole)
	if err != nil {
		return err
	}
	defer atspiUnref(focus)
	candidate, action, index, name, err := findATSEscapeAction(ctx, window, focus)
	if err != nil {
		return err
	}
	defer atspiUnref(candidate)
	defer atspiUnref(action)
	currentFocus, err := findATSFocusedWith(ctx, window, x11AccessibleRole)
	if err != nil {
		return err
	}
	sameFocus := currentFocus == focus
	atspiUnref(currentFocus)
	if !sameFocus {
		return ErrStaleObservation
	}
	currentCandidate, currentAction, currentIndex, currentName, err := findATSEscapeAction(ctx, window, focus)
	if err != nil {
		return err
	}
	sameAction := currentCandidate == candidate && currentIndex == index && currentName == name
	atspiUnref(currentCandidate)
	atspiUnref(currentAction)
	if !sameAction {
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
	var failure uintptr
	ok := atspiDoAction(action, index, &failure) != 0
	if atspiError(failure) != nil || !ok {
		return fmt.Errorf("%w: Linux cancel action was refused", errLinuxSemanticAttempted)
	}
	return nil
}
