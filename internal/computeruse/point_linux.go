//go:build linux && (amd64 || arm64)

package computeruse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Linux point clicks target a verified AT-SPI descendant directly; they do not
// inject global mouse input. Controls without a semantic action fail closed.
var atspiPointOnce sync.Once
var atspiPointErr error
var atspiComponent func(uintptr) uintptr
var atspiExtents func(uintptr, int32, *uintptr) uintptr
var atspiAtPoint func(uintptr, int32, int32, int32, *uintptr) uintptr
var errLinuxSemanticAttempted = fmt.Errorf("%w: Linux semantic input may already have been dispatched", ErrUnavailable)

func loadATSPoint() error {
	atspiPointOnce.Do(func() {
		lib, err := purego.Dlopen("libatspi.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			atspiPointErr = ErrUnavailable
			return
		}
		for _, binding := range []struct {
			name string
			out  any
		}{
			{"atspi_accessible_get_component_iface", &atspiComponent},
			{"atspi_component_get_extents", &atspiExtents},
			{"atspi_component_get_accessible_at_point", &atspiAtPoint},
		} {
			address, err := purego.Dlsym(lib, binding.name)
			if err != nil || address == 0 {
				atspiPointErr = ErrUnavailable
				return
			}
			purego.RegisterFunc(binding.out, address)
		}
	})
	return atspiPointErr
}

type linuxFrameBinding struct {
	Target string
	Width  int
	Height int
	Digest [32]byte
	HasX11 bool
	X11    x11FrameIdentity
}

type atspiRect struct {
	X, Y, Width, Height int32
}

func (b *linuxATSPIBackend) CaptureBound(ctx context.Context, target Target) ([]byte, string, error) {
	data, err := b.Capture(ctx, target)
	if err != nil {
		return nil, "", err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > MaxFramePixels {
		return nil, "", errors.New("Linux window frame is invalid")
	}
	binding := linuxFrameBinding{Target: target.ID, Width: config.Width, Height: config.Height, Digest: sha256.Sum256(data)}
	if x11Session() {
		if identity, err := b.captureX11Identity(ctx, target, config.Width, config.Height); err == nil {
			binding.HasX11, binding.X11 = true, identity
		} else if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
	}
	if err := b.Validate(ctx, target, false); err != nil {
		return nil, "", err
	}
	b.mu.Lock()
	if !b.closed && b.portalApproved && b.portalTarget == target.ID {
		b.x11Ready = binding.HasX11
	}
	b.mu.Unlock()
	token, err := json.Marshal(binding)
	if err != nil {
		return nil, "", err
	}
	return data, string(token), nil
}

func atspiTargetContains(ctx context.Context, window atspiWindow, wanted uintptr) bool {
	budget := 500
	seen := make(map[uintptr]bool)
	var visit func(uintptr, int) bool
	visit = func(node uintptr, depth int) bool {
		if ctx.Err() != nil || budget <= 0 || depth > 16 || seen[node] {
			return false
		}
		seen[node] = true
		budget--
		pid, pidErr := atspiGetPID(node)
		role, roleErr := atspiGetString(node, atspiRole)
		if pidErr != nil || roleErr != nil || pid != window.pid || secureATSPIRole(role) || atspiState(node, 6) {
			return false
		}
		if node == wanted {
			return true
		}
		count, err := atspiChildren(node)
		if err != nil {
			return false
		}
		for i := 0; i < count; i++ {
			child, err := atspiGetChild(node, i)
			if err != nil {
				continue
			}
			found := visit(child, depth+1)
			atspiUnref(child)
			if found {
				return true
			}
		}
		return false
	}
	return visit(window.object, 0)
}

func atspiPointTargetable(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	if secureATSPIRole(role) {
		return false
	}
	switch role {
	case "push button", "toggle button", "check box", "radio button", "link", "menu item", "check menu item", "radio menu item", "page tab", "list item", "tree item":
		return true
	default:
		return false
	}
}

// ClickFrame prefers a target-owned semantic action. A pinned X11 frame can
// fall back to a window-addressed synthetic event before any action was tried.
func (b *linuxATSPIBackend) ClickFrame(ctx context.Context, target Target, token string, x, y float64) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) || x < 0 || x >= 1 || y < 0 || y >= 1 {
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
	err = b.clickSemanticFrame(ctx, target, previous, x, y)
	if previous.HasX11 && errors.Is(err, ErrUnavailable) && !errors.Is(err, errLinuxSemanticAttempted) {
		return b.x11Dispatch(ctx, target, previous, "click", x, y, 0, 0, "")
	}
	return err
}

func (b *linuxATSPIBackend) clickSemanticFrame(ctx context.Context, target Target, previous linuxFrameBinding, x, y float64) error {
	if err := loadATSPoint(); err != nil {
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
	p := atspiExtents(component, 1, &failure) // coordinates relative to the target window
	if atspiError(failure) != nil || p == 0 {
		if p != 0 {
			atspiFree(p)
		}
		return ErrUnavailable
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	rect := *(*atspiRect)(base)
	atspiFree(p)
	if rect.Width <= 0 || rect.Height <= 0 || int64(rect.Width) != int64(previous.Width) || int64(rect.Height) != int64(previous.Height) {
		return errors.New("Linux window and screenshot geometry do not match")
	}
	px := int64(rect.X) + int64(math.Floor(x*float64(rect.Width)))
	py := int64(rect.Y) + int64(math.Floor(y*float64(rect.Height)))
	if px < math.MinInt32 || px > math.MaxInt32 || py < math.MinInt32 || py > math.MaxInt32 {
		return ErrNotAllowed
	}
	failure = 0
	object := atspiAtPoint(component, int32(px), int32(py), 1, &failure)
	if atspiError(failure) != nil || object == 0 {
		if object != 0 {
			atspiUnref(object)
		}
		return ErrUnavailable
	}
	defer atspiUnref(object)
	if !atspiTargetContains(ctx, window, object) {
		return ErrTargetLost
	}
	initialRole, roleErr := atspiGetString(object, atspiRole)
	if roleErr != nil {
		return ErrUnavailable
	}
	if !atspiPointTargetable(initialRole) {
		if previous.HasX11 && x11AccessibleRole(initialRole) {
			return ErrUnavailable
		}
		return ErrNotAllowed
	}
	initialName, nameErr := atspiGetString(object, atspiName)
	if nameErr != nil {
		return ErrUnavailable
	}
	check := func() error {
		if !atspiTargetContains(ctx, window, object) || !atspiState(object, 8) || !atspiState(object, 24) || !atspiState(object, 25) || !atspiState(object, 30) {
			return ErrTargetLost
		}
		role, err := atspiGetString(object, atspiRole)
		if err != nil || role != initialRole || !atspiPointTargetable(role) {
			return ErrNotAllowed
		}
		name, err := atspiGetString(object, atspiName)
		if err != nil || name != initialName {
			return ErrStaleObservation
		}
		var failure uintptr
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
		hit := atspiAtPoint(component, int32(px), int32(py), 1, &failure)
		if atspiError(failure) != nil || hit == 0 {
			if hit != 0 {
				atspiUnref(hit)
			}
			return ErrTargetLost
		}
		same := hit == object
		atspiUnref(hit)
		if !same {
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
	if err := check(); err != nil {
		return err
	}
	action := atspiAction(object)
	if action == 0 {
		return ErrUnavailable
	}
	defer atspiUnref(action)
	failure = 0
	count := int(atspiActionCount(action, &failure))
	if atspiError(failure) != nil || count < 1 || count > 32 {
		return ErrUnavailable
	}
	index := -1
	for i := 0; i < count; i++ {
		failure = 0
		name := atspiString(atspiActionName(action, int32(i), &failure))
		if atspiError(failure) != nil {
			return ErrUnavailable
		}
		switch strings.ToLower(name) {
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
	if err := check(); err != nil {
		return err
	}
	failure = 0
	ok := atspiDoAction(action, int32(index), &failure) != 0
	if atspiError(failure) != nil || !ok {
		return fmt.Errorf("%w: Linux point action was refused", errLinuxSemanticAttempted)
	}
	return nil
}
