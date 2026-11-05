//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var atspiScrollOnce sync.Once
var atspiScrollErr error
var atspiParent func(uintptr, *uintptr) uintptr
var atspiValue func(uintptr) uintptr
var atspiValueCurrent func(uintptr, *uintptr) float64
var atspiValueMinimum func(uintptr, *uintptr) float64
var atspiValueMaximum func(uintptr, *uintptr) float64
var atspiValueIncrement func(uintptr, *uintptr) float64
var atspiValueSet func(uintptr, float64, *uintptr) int32

func loadATSScroll() error {
	atspiScrollOnce.Do(func() {
		lib, err := purego.Dlopen("libatspi.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			atspiScrollErr = ErrUnavailable
			return
		}
		for _, binding := range []struct {
			name string
			out  any
		}{
			{"atspi_accessible_get_parent", &atspiParent},
			{"atspi_accessible_get_value_iface", &atspiValue},
			{"atspi_value_get_current_value", &atspiValueCurrent},
			{"atspi_value_get_minimum_value", &atspiValueMinimum},
			{"atspi_value_get_maximum_value", &atspiValueMaximum},
			{"atspi_value_get_minimum_increment", &atspiValueIncrement},
			{"atspi_value_set_current_value", &atspiValueSet},
		} {
			address, err := purego.Dlsym(lib, binding.name)
			if err != nil || address == 0 {
				atspiScrollErr = ErrUnavailable
				return
			}
			purego.RegisterFunc(binding.out, address)
		}
	})
	return atspiScrollErr
}

func atspiScrollParent(object uintptr) (uintptr, error) {
	var failure uintptr
	parent := atspiParent(object, &failure)
	if atspiError(failure) != nil || parent == 0 {
		if parent != 0 {
			atspiUnref(parent)
		}
		return 0, ErrUnavailable
	}
	return parent, nil
}

// findATSScrollBar finds a unique visible scrollbar in the nearest containing
// scroll pane. Nested scroll panes do not donate their bars to their parents.
// All returned references are owned by the caller.
func findATSScrollBar(ctx context.Context, window atspiWindow, hit uintptr, vertical bool) (uintptr, uintptr, error) {
	current := atspiRef(hit)
	if current == 0 {
		return 0, 0, ErrTargetLost
	}
	defer func() {
		if current != 0 {
			atspiUnref(current)
		}
	}()
	seenParents := make(map[uintptr]bool)
	for depth := 0; depth < 16 && current != 0; depth++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if seenParents[current] || !atspiTargetContains(ctx, window, current) {
			return 0, 0, ErrTargetLost
		}
		seenParents[current] = true
		role, err := atspiGetString(current, atspiRole)
		if err != nil || secureATSPIRole(role) {
			return 0, 0, ErrNotAllowed
		}
		if role == "scroll pane" {
			var match uintptr
			ambiguous := false
			budget := 128
			visited := make(map[uintptr]bool)
			var visit func(uintptr, int)
			visit = func(node uintptr, depth int) {
				if ctx.Err() != nil || budget <= 0 || depth > 8 || visited[node] || ambiguous {
					return
				}
				visited[node] = true
				budget--
				pid, pidErr := atspiGetPID(node)
				kind, roleErr := atspiGetString(node, atspiRole)
				if pidErr != nil || roleErr != nil || pid != window.pid || secureATSPIRole(kind) || atspiState(node, 6) || (depth > 0 && kind == "scroll pane") {
					return
				}
				if kind == "scroll bar" {
					orientation := int32(14)
					if vertical {
						orientation = 29
					}
					if atspiState(node, orientation) && atspiState(node, 8) && atspiState(node, 24) && atspiState(node, 25) && atspiState(node, 30) {
						if match != 0 {
							ambiguous = true
							return
						}
						match = atspiRef(node)
					}
					return
				}
				count, err := atspiChildren(node)
				if err != nil {
					return
				}
				for i := 0; i < count; i++ {
					child, err := atspiGetChild(node, i)
					if err != nil {
						continue
					}
					visit(child, depth+1)
					atspiUnref(child)
				}
			}
			visit(current, 0)
			if ambiguous || budget <= 0 || ctx.Err() != nil {
				if match != 0 {
					atspiUnref(match)
				}
				return 0, 0, ErrUnavailable
			}
			if match != 0 {
				pane := atspiRef(current)
				if pane == 0 {
					atspiUnref(match)
					return 0, 0, ErrTargetLost
				}
				return pane, match, nil
			}
		}
		if current == window.object {
			break
		}
		parent, err := atspiScrollParent(current)
		if err != nil {
			return 0, 0, err
		}
		atspiUnref(current)
		current = parent
	}
	return 0, 0, ErrUnavailable
}

// ScrollFrame prefers an unambiguous target-owned accessibility scrollbar;
// a pinned X11 frame can use window-addressed events when it is unavailable.
func (b *linuxATSPIBackend) ScrollFrame(ctx context.Context, target Target, token string, x, y float64, dx, dy int) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < 0 || x >= 1 || y < 0 || y >= 1 || dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
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
	if dx != 0 && dy != 0 {
		if previous.HasX11 {
			return b.x11Dispatch(ctx, target, previous, "scroll", x, y, dx, dy, "")
		}
		return errors.New("Linux semantic scrolling requires one axis per action")
	}
	err = b.scrollSemanticFrame(ctx, target, previous, x, y, dx, dy)
	if previous.HasX11 && errors.Is(err, ErrUnavailable) {
		return b.x11Dispatch(ctx, target, previous, "scroll", x, y, dx, dy, "")
	}
	return err
}

func (b *linuxATSPIBackend) scrollSemanticFrame(ctx context.Context, target Target, previous linuxFrameBinding, x, y float64, dx, dy int) error {
	if err := loadATSPoint(); err != nil {
		return err
	}
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
	component := atspiComponent(window.object)
	if component == 0 {
		return ErrUnavailable
	}
	defer atspiUnref(component)
	var failure uintptr
	ptr := atspiExtents(component, 1, &failure)
	if atspiError(failure) != nil || ptr == 0 {
		if ptr != 0 {
			atspiFree(ptr)
		}
		return ErrUnavailable
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	rect := *(*atspiRect)(base)
	atspiFree(ptr)
	if rect.Width <= 0 || rect.Height <= 0 || int64(rect.Width) != int64(previous.Width) || int64(rect.Height) != int64(previous.Height) {
		return errors.New("Linux window and screenshot geometry do not match")
	}
	px := int64(rect.X) + int64(math.Floor(x*float64(rect.Width)))
	py := int64(rect.Y) + int64(math.Floor(y*float64(rect.Height)))
	if px < math.MinInt32 || px > math.MaxInt32 || py < math.MinInt32 || py > math.MaxInt32 {
		return ErrNotAllowed
	}
	failure = 0
	hit := atspiAtPoint(component, int32(px), int32(py), 1, &failure)
	if atspiError(failure) != nil || hit == 0 {
		if hit != 0 {
			atspiUnref(hit)
		}
		return ErrUnavailable
	}
	defer atspiUnref(hit)
	pane, bar, err := findATSScrollBar(ctx, window, hit, dy != 0)
	if err != nil {
		return err
	}
	defer atspiUnref(pane)
	defer atspiUnref(bar)
	value := atspiValue(bar)
	if value == 0 {
		return ErrUnavailable
	}
	defer atspiUnref(value)
	read := func(fn func(uintptr, *uintptr) float64) (float64, error) {
		var failure uintptr
		result := fn(value, &failure)
		if atspiError(failure) != nil || math.IsNaN(result) || math.IsInf(result, 0) {
			return 0, ErrUnavailable
		}
		return result, nil
	}
	position, err := read(atspiValueCurrent)
	if err != nil {
		return err
	}
	minimum, err := read(atspiValueMinimum)
	if err != nil {
		return err
	}
	maximum, err := read(atspiValueMaximum)
	if err != nil {
		return err
	}
	increment, err := read(atspiValueIncrement)
	if err != nil || increment <= 0 || maximum <= minimum || position < minimum || position > maximum {
		return ErrUnavailable
	}
	steps := float64(dx)
	if dy != 0 {
		steps = -float64(dy)
	}
	next := math.Max(minimum, math.Min(maximum, position+steps*increment))
	if next == position {
		return errors.New("Linux scrollbar is already at its limit")
	}
	if !atspiTargetContains(ctx, window, hit) || !atspiTargetContains(ctx, window, pane) || !atspiTargetContains(ctx, window, bar) {
		return ErrTargetLost
	}
	nested := window
	nested.object = pane
	if !atspiTargetContains(ctx, nested, hit) || !atspiTargetContains(ctx, nested, bar) {
		return ErrTargetLost
	}
	role, err := atspiGetString(bar, atspiRole)
	orientation := int32(14)
	if dy != 0 {
		orientation = 29
	}
	if err != nil || role != "scroll bar" || !atspiState(bar, orientation) || !atspiState(bar, 8) || !atspiState(bar, 24) || !atspiState(bar, 25) || !atspiState(bar, 30) {
		return ErrTargetLost
	}
	currentPosition, err := read(atspiValueCurrent)
	if err != nil || currentPosition != position {
		return ErrStaleObservation
	}
	currentMinimum, minErr := read(atspiValueMinimum)
	currentMaximum, maxErr := read(atspiValueMaximum)
	currentIncrement, incrementErr := read(atspiValueIncrement)
	if minErr != nil || maxErr != nil || incrementErr != nil || currentMinimum != minimum || currentMaximum != maximum || currentIncrement != increment {
		return ErrStaleObservation
	}
	failure = 0
	currentRectPtr := atspiExtents(component, 1, &failure)
	if atspiError(failure) != nil || currentRectPtr == 0 {
		if currentRectPtr != 0 {
			atspiFree(currentRectPtr)
		}
		return ErrTargetLost
	}
	currentBase := *(*unsafe.Pointer)(unsafe.Pointer(&currentRectPtr))
	currentRect := *(*atspiRect)(currentBase)
	atspiFree(currentRectPtr)
	if currentRect != rect {
		return ErrStaleObservation
	}
	failure = 0
	currentHit := atspiAtPoint(component, int32(px), int32(py), 1, &failure)
	if atspiError(failure) != nil || currentHit == 0 {
		if currentHit != 0 {
			atspiUnref(currentHit)
		}
		return ErrTargetLost
	}
	sameHit := currentHit == hit
	atspiUnref(currentHit)
	if !sameHit {
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
	ok := atspiValueSet(value, next, &failure) != 0
	if atspiError(failure) != nil || !ok {
		return errors.New("Linux semantic scroll was refused")
	}
	return nil
}
