//go:build linux && (amd64 || arm64)

package computeruse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
)

// libatspi uses process-global D-Bus state. The mutex serializes native calls,
// and the library timeout bounds each synchronous request.
var atspiOnce sync.Once
var atspiMu sync.Mutex
var atspiLoadErr error
var atspiInit func() int32
var atspiTimeout func(int32, int32)
var atspiDesktop func(int32) uintptr
var atspiCount func(uintptr, *uintptr) int32
var atspiChild func(uintptr, int32, *uintptr) uintptr
var atspiName func(uintptr, *uintptr) uintptr
var atspiRole func(uintptr, *uintptr) uintptr
var atspiPID func(uintptr, *uintptr) uint32
var atspiStates func(uintptr) uintptr
var atspiContains func(uintptr, int32) int32
var atspiAction func(uintptr) uintptr
var atspiActionCount func(uintptr, *uintptr) int32
var atspiActionName func(uintptr, int32, *uintptr) uintptr
var atspiDoAction func(uintptr, int32, *uintptr) int32
var atspiEditable func(uintptr) uintptr
var atspiSetText func(uintptr, *byte, *uintptr) int32
var atspiRef func(uintptr) uintptr
var atspiUnref func(uintptr)
var atspiFree func(uintptr)
var atspiErrorFree func(uintptr)

func loadATSPI() error {
	atspiOnce.Do(func() {
		var handles []uintptr
		for _, name := range []string{"libatspi.so.0", "libgobject-2.0.so.0", "libglib-2.0.so.0"} {
			h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				atspiLoadErr = fmt.Errorf("%w: Linux accessibility library %s is unavailable", ErrUnavailable, name)
				return
			}
			handles = append(handles, h)
		}
		bindings := []struct {
			lib  uintptr
			name string
			out  any
		}{
			{handles[0], "atspi_init", &atspiInit},
			{handles[0], "atspi_set_timeout", &atspiTimeout},
			{handles[0], "atspi_get_desktop", &atspiDesktop},
			{handles[0], "atspi_accessible_get_child_count", &atspiCount},
			{handles[0], "atspi_accessible_get_child_at_index", &atspiChild},
			{handles[0], "atspi_accessible_get_name", &atspiName},
			{handles[0], "atspi_accessible_get_role_name", &atspiRole},
			{handles[0], "atspi_accessible_get_process_id", &atspiPID},
			{handles[0], "atspi_accessible_get_state_set", &atspiStates},
			{handles[0], "atspi_state_set_contains", &atspiContains},
			{handles[0], "atspi_accessible_get_action_iface", &atspiAction},
			{handles[0], "atspi_action_get_n_actions", &atspiActionCount},
			{handles[0], "atspi_action_get_action_name", &atspiActionName},
			{handles[0], "atspi_action_do_action", &atspiDoAction},
			{handles[0], "atspi_accessible_get_editable_text_iface", &atspiEditable},
			{handles[0], "atspi_editable_text_set_text_contents", &atspiSetText},
			{handles[1], "g_object_ref", &atspiRef},
			{handles[1], "g_object_unref", &atspiUnref},
			{handles[2], "g_free", &atspiFree},
			{handles[2], "g_error_free", &atspiErrorFree},
		}
		for _, binding := range bindings {
			address, err := purego.Dlsym(binding.lib, binding.name)
			if err != nil || address == 0 {
				atspiLoadErr = fmt.Errorf("%w: Linux accessibility symbol %s is unavailable", ErrUnavailable, binding.name)
				return
			}
			purego.RegisterFunc(binding.out, address)
		}
		status := atspiInit()
		if status != 0 && status != 1 {
			atspiLoadErr = fmt.Errorf("%w: Linux accessibility service could not be initialized", ErrUnavailable)
			return
		}
		atspiTimeout(1000, 1000)
	})
	return atspiLoadErr
}

type atspiWindow struct {
	object      uintptr
	pid         int
	start       uint64
	executable  string
	application string
	title       string
}

type linuxATSPIBackend struct {
	mu              sync.Mutex
	windows         map[string]atspiWindow
	refs            map[string]uintptr
	snapshot        string
	targetID        string
	portal          *linuxPortal
	portalTarget    string
	portalCandidate string
	portalApproved  bool
	portalSequence  uint64
	x11Ready        bool
	closed          bool
}

func newLinuxATSPIBackend() (*linuxATSPIBackend, error) {
	if err := loadATSPI(); err != nil {
		return nil, err
	}
	return &linuxATSPIBackend{windows: make(map[string]atspiWindow)}, nil
}

func atspiString(p uintptr) string {
	if p == 0 {
		return ""
	}
	defer atspiFree(p)
	var out []byte
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	for i := 0; i < 4096; i++ {
		b := *(*byte)(unsafe.Add(base, i))
		if b == 0 {
			return strings.ToValidUTF8(string(out), "�")
		}
		out = append(out, b)
	}
	return ""
}

func atspiError(p uintptr) error {
	if p == 0 {
		return nil
	}
	atspiErrorFree(p)
	return errors.New("Linux accessibility call failed")
}

func atspiGetString(object uintptr, fn func(uintptr, *uintptr) uintptr) (string, error) {
	var failure uintptr
	value := fn(object, &failure)
	if err := atspiError(failure); err != nil {
		if value != 0 {
			atspiFree(value)
		}
		return "", err
	}
	return atspiString(value), nil
}

func atspiGetPID(object uintptr) (int, error) {
	var failure uintptr
	pid := int(atspiPID(object, &failure))
	if atspiError(failure) != nil || pid <= 0 {
		return 0, ErrTargetLost
	}
	return pid, nil
}

func atspiChildren(object uintptr) (int, error) {
	var failure uintptr
	count := int(atspiCount(object, &failure))
	if atspiError(failure) != nil || count < 0 {
		return 0, ErrTargetLost
	}
	return min(count, 256), nil
}

func atspiGetChild(object uintptr, index int) (uintptr, error) {
	var failure uintptr
	child := atspiChild(object, int32(index), &failure)
	if atspiError(failure) != nil || child == 0 {
		if child != 0 {
			atspiUnref(child)
		}
		return 0, ErrTargetLost
	}
	return child, nil
}

func atspiState(object uintptr, state int32) bool {
	set := atspiStates(object)
	if set == 0 {
		return false
	}
	defer atspiUnref(set)
	return atspiContains(set, state) != 0
}

func linuxProcessIdentity(pid int) (uint64, string, error) {
	if pid <= 0 || pid == os.Getpid() {
		return 0, "", ErrTargetLost
	}
	root := fmt.Sprintf("/proc/%d", pid)
	info, err := os.Stat(root)
	if err != nil {
		return 0, "", ErrTargetLost
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return 0, "", ErrTargetLost
	}
	exe, err := os.Readlink(filepath.Join(root, "exe"))
	if err != nil || !filepath.IsAbs(exe) || protectedLinuxExecutable(exe) {
		return 0, "", ErrTargetLost
	}
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0, "", ErrTargetLost
	}
	start, err := linuxProcessStart(string(data), pid)
	return start, exe, err
}

func protectedLinuxExecutable(exe string) bool {
	base := strings.ToLower(filepath.Base(exe))
	for _, prefix := range []string{"golder", "tuios", "gnome-terminal", "kgx", "konsole", "xterm", "kitty", "alacritty", "wezterm", "ghostty", "polkit", "gnome-shell", "gnome-control-center", "xdg-desktop-portal"} {
		if base == prefix || strings.HasPrefix(base, prefix+"-") {
			return true
		}
	}
	return false
}

func (b *linuxATSPIBackend) Targets(ctx context.Context) ([]Target, error) {
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := atspiDesktop(0)
	if root == 0 {
		return nil, fmt.Errorf("%w: no AT-SPI desktop", ErrUnavailable)
	}
	defer atspiUnref(root)
	count, err := atspiChildren(root)
	if err != nil {
		return nil, err
	}
	next := make(map[string]atspiWindow)
	var targets []Target
	for i := 0; i < count && len(targets) < 128; i++ {
		if err := ctx.Err(); err != nil {
			for _, window := range next {
				atspiUnref(window.object)
			}
			return nil, err
		}
		app, err := atspiGetChild(root, i)
		if err != nil {
			continue
		}
		pid, pidErr := atspiGetPID(app)
		start, exe, identityErr := linuxProcessIdentity(pid)
		name, nameErr := atspiGetString(app, atspiName)
		children, childrenErr := atspiChildren(app)
		if pidErr == nil && identityErr == nil && nameErr == nil && childrenErr == nil && name != "" {
			for j := 0; j < children && len(targets) < 128 && ctx.Err() == nil; j++ {
				window, err := atspiGetChild(app, j)
				if err != nil {
					continue
				}
				role, roleErr := atspiGetString(window, atspiRole)
				windowPID, windowErr := atspiGetPID(window)
				title, titleErr := atspiGetString(window, atspiName)
				if roleErr != nil || windowErr != nil || titleErr != nil || windowPID != pid || (role != "frame" && role != "window" && role != "dialog") || !atspiState(window, 25) || !atspiState(window, 30) || atspiState(window, 6) {
					atspiUnref(window)
					continue
				}
				var nonce [16]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					atspiUnref(window)
					atspiUnref(app)
					for _, old := range next {
						atspiUnref(old.object)
					}
					return nil, err
				}
				id := hex.EncodeToString(nonce[:])
				next[id] = atspiWindow{object: window, pid: pid, start: start, executable: exe, application: name, title: title}
				targets = append(targets, Target{ID: id, Application: name, Window: title, Detail: fmt.Sprintf("PID %d · Window %d", pid, j+1)})
			}
		}
		atspiUnref(app)
	}
	if err := ctx.Err(); err != nil {
		for _, window := range next {
			atspiUnref(window.object)
		}
		return nil, err
	}
	b.clearRefs()
	for _, window := range b.windows {
		atspiUnref(window.object)
	}
	b.windows = next
	b.portalSequence++
	if b.portal != nil {
		old := b.portal
		b.portal = nil
		go old.close()
	}
	b.portalTarget, b.portalCandidate, b.portalApproved = "", "", false
	b.x11Ready = false
	return targets, nil
}

func (b *linuxATSPIBackend) validate(ctx context.Context, target Target) (atspiWindow, error) {
	if err := ctx.Err(); err != nil {
		return atspiWindow{}, err
	}
	w, ok := b.windows[target.ID]
	if b.closed || !ok || target.Application != w.application || target.Window != w.title {
		return atspiWindow{}, ErrTargetLost
	}
	start, exe, err := linuxProcessIdentity(w.pid)
	if err != nil || start != w.start || exe != w.executable {
		return atspiWindow{}, ErrTargetLost
	}
	pid, err := atspiGetPID(w.object)
	if err != nil || pid != w.pid || !atspiState(w.object, 25) || !atspiState(w.object, 30) || atspiState(w.object, 6) {
		return atspiWindow{}, ErrTargetLost
	}
	return w, nil
}

func (b *linuxATSPIBackend) Validate(ctx context.Context, target Target, _ bool) error {
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.validate(ctx, target)
	return err
}

func (b *linuxATSPIBackend) clearRefs() {
	for _, object := range b.refs {
		atspiUnref(object)
	}
	b.refs, b.snapshot, b.targetID = nil, "", ""
}

func (b *linuxATSPIBackend) Close() error {
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.portalSequence++
		if b.portal != nil {
			old := b.portal
			b.portal = nil
			go old.close()
		}
		b.portalTarget, b.portalCandidate, b.portalApproved = "", "", false
		b.x11Ready = false
		b.clearRefs()
		for _, window := range b.windows {
			atspiUnref(window.object)
		}
		b.windows = nil
	}
	return nil
}

func secureATSPIRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "" || role == "unknown" || role == "invalid" || strings.Contains(role, "password") || strings.Contains(role, "protected")
}

func (b *linuxATSPIBackend) Snapshot(ctx context.Context, target Target) (string, []Element, bool, error) {
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return "", nil, false, err
	}
	b.clearRefs()
	refs := make(map[string]uintptr)
	var elements []Element
	seen := make(map[uintptr]bool)
	truncated := false
	var visit func(uintptr, int) error
	visit = func(object uintptr, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(elements) >= 128 || depth > 16 {
			truncated = true
			return nil
		}
		if seen[object] {
			truncated = true
			return nil
		}
		seen[object] = true
		pid, pidErr := atspiGetPID(object)
		role, roleErr := atspiGetString(object, atspiRole)
		if pidErr != nil || roleErr != nil || pid != w.pid {
			truncated = true
			return nil
		}
		state := atspiStates(object)
		if state == 0 {
			truncated = true
			return nil
		}
		defunct := atspiContains(state, 6) != 0
		secure := secureATSPIRole(role)
		editable := !secure && atspiContains(state, 7) != 0
		enabled := atspiContains(state, 8) != 0 && atspiContains(state, 24) != 0 && atspiContains(state, 25) != 0 && atspiContains(state, 30) != 0
		atspiUnref(state)
		if defunct {
			truncated = true
			return nil
		}
		name := ""
		if !secure && !editable {
			var nameErr error
			name, nameErr = atspiGetString(object, atspiName)
			if nameErr != nil {
				truncated = true
			}
		}
		ref := fmt.Sprintf("@e%d", len(elements)+1)
		owned := atspiRef(object)
		if owned == 0 {
			return ErrTargetLost
		}
		refs[ref] = owned
		elements = append(elements, Element{Ref: ref, Role: role, Name: name, Secure: secure, Editable: editable, Enabled: enabled})
		if secure {
			return nil
		}
		count, err := atspiChildren(object)
		if err != nil {
			truncated = true
			return nil
		}
		for i := 0; i < count; i++ {
			child, err := atspiGetChild(object, i)
			if err != nil {
				truncated = true
				continue
			}
			err = visit(child, depth+1)
			atspiUnref(child)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(w.object, 0); err != nil || len(elements) == 0 {
		for _, object := range refs {
			atspiUnref(object)
		}
		if err == nil {
			err = ErrTargetLost
		}
		return "", nil, false, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		for _, object := range refs {
			atspiUnref(object)
		}
		return "", nil, false, err
	}
	b.refs, b.snapshot, b.targetID = refs, hex.EncodeToString(nonce[:]), target.ID
	return b.snapshot, elements, truncated, nil
}

func (*linuxATSPIBackend) SupportsAction(action string) bool {
	return action == "click" || action == "fill"
}

func (b *linuxATSPIBackend) Act(ctx context.Context, target Target, snapshotID, ref, action, text string) error {
	if (action != "click" && action != "fill") || (action == "click" && text != "") || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return ErrNotAllowed
	}
	atspiMu.Lock()
	defer atspiMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	w, err := b.validate(ctx, target)
	if err != nil {
		return err
	}
	object, ok := b.refs[ref]
	if !ok || b.snapshot != snapshotID || b.targetID != target.ID {
		return ErrStaleObservation
	}
	object = atspiRef(object)
	if object == 0 {
		return ErrTargetLost
	}
	b.clearRefs()
	defer atspiUnref(object)
	budget := 500
	var contains func(uintptr, int) bool
	contains = func(node uintptr, depth int) bool {
		if ctx.Err() != nil || budget <= 0 || depth > 16 {
			return false
		}
		budget--
		pid, pidErr := atspiGetPID(node)
		role, roleErr := atspiGetString(node, atspiRole)
		if pidErr != nil || roleErr != nil || pid != w.pid || secureATSPIRole(role) || atspiState(node, 6) {
			return false
		}
		if node == object {
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
			found := contains(child, depth+1)
			atspiUnref(child)
			if found {
				return true
			}
		}
		return false
	}
	recheck := func() error {
		budget = 500
		if !contains(w.object, 0) || !atspiState(object, 8) || !atspiState(object, 24) || !atspiState(object, 25) || !atspiState(object, 30) {
			return ErrTargetLost
		}
		if _, err := b.validate(ctx, target); err != nil {
			return err
		}
		return ctx.Err()
	}
	if err := recheck(); err != nil {
		return err
	}
	var failure uintptr
	if action == "fill" {
		if !atspiState(object, 7) || strings.ContainsRune(text, '\x00') {
			return ErrNotAllowed
		}
		iface := atspiEditable(object)
		if iface == 0 {
			return ErrUnavailable
		}
		defer atspiUnref(iface)
		value := append([]byte(text), 0)
		if err := recheck(); err != nil {
			return err
		}
		ok := atspiSetText(iface, &value[0], &failure) != 0
		if atspiError(failure) != nil || !ok {
			return errors.New("Linux accessibility text replacement was refused")
		}
	} else {
		iface := atspiAction(object)
		if iface == 0 {
			return ErrUnavailable
		}
		defer atspiUnref(iface)
		count := int(atspiActionCount(iface, &failure))
		if atspiError(failure) != nil || count < 1 || count > 32 {
			return ErrUnavailable
		}
		chosen := -1
		for i := 0; i < count; i++ {
			failure = 0
			name := atspiString(atspiActionName(iface, int32(i), &failure))
			if atspiError(failure) != nil {
				return ErrUnavailable
			}
			switch strings.ToLower(name) {
			case "click", "press", "activate":
				if chosen >= 0 {
					return ErrUnavailable
				}
				chosen = i
			}
		}
		if chosen < 0 || ctx.Err() != nil {
			return ErrUnavailable
		}
		if err := recheck(); err != nil {
			return err
		}
		failure = 0
		ok := atspiDoAction(iface, int32(chosen), &failure) != 0
		if atspiError(failure) != nil || !ok {
			return errors.New("Linux accessibility action was refused")
		}
	}
	return nil
}
