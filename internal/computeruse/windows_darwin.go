//go:build darwin && cgo

package computeruse

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework Foundation -framework AppKit -framework ScreenCaptureKit -framework ImageIO
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <sys/proc_info.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>
#include <math.h>
#include <stdatomic.h>

int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length);
int cu_app_allowed(int32_t pid);

typedef struct {
	uint32_t window_id;
	int32_t pid;
	uint64_t start_sec;
	uint64_t start_usec;
	char application[256];
	char title[512];
} cu_window;

static void cu_string(CFTypeRef v, char *out, size_t n) {
	if (v && CFGetTypeID(v) == CFStringGetTypeID()) {
		if (!CFStringGetCString((CFStringRef)v, out, n, kCFStringEncodingUTF8)) out[0] = 0;
	}
}

static int cu_process_start(int32_t pid, uint64_t *sec, uint64_t *usec) {
	struct proc_bsdinfo info = {0};
	int n = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info));
	if (n != sizeof(info)) return 0;
	*sec = info.pbi_start_tvsec;
	*usec = info.pbi_start_tvusec;
	return 1;
}

static int cu_windows(cu_window *out, int cap) {
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return -1;
	int count = 0;
	CFIndex total = CFArrayGetCount(list);
	for (CFIndex i = 0; i < total && count < cap; i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		int32_t pid = 0, layer = 0;
		uint32_t window_id = 0;
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef w = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		if (!p || !w || !l || !CFNumberGetValue(p, kCFNumberSInt32Type, &pid) ||
			!CFNumberGetValue(w, kCFNumberSInt32Type, &window_id) ||
			!CFNumberGetValue(l, kCFNumberSInt32Type, &layer) ||
			pid <= 0 || pid == getpid() || layer != 0 || window_id == 0 || !cu_app_allowed(pid)) continue;
		CGRect bounds;
		CFDictionaryRef bd = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		if (!bd || !CGRectMakeWithDictionaryRepresentation(bd, &bounds) || bounds.size.width < 1 || bounds.size.height < 1) continue;
		cu_window entry = {0};
		entry.pid = pid;
		entry.window_id = window_id;
		if (!cu_process_start(pid, &entry.start_sec, &entry.start_usec)) continue;
		cu_string(CFDictionaryGetValue(d, kCGWindowOwnerName), entry.application, sizeof(entry.application));
		cu_string(CFDictionaryGetValue(d, kCGWindowName), entry.title, sizeof(entry.title));
		if (entry.application[0] == 0) continue;
		out[count++] = entry;
	}
	CFRelease(list);
	return count;
}

static int cu_window_exists(uint32_t window_id, int32_t pid, uint64_t sec, uint64_t usec) {
	uint64_t now_sec = 0, now_usec = 0;
	if (!cu_app_allowed(pid) || !cu_process_start(pid, &now_sec, &now_usec) || now_sec != sec || now_usec != usec) return 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return 0;
	int found = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		int32_t current_pid = 0;
		uint32_t current_id = 0;
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef w = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		if (p && w && CFNumberGetValue(p, kCFNumberSInt32Type, &current_pid) &&
			CFNumberGetValue(w, kCFNumberSInt32Type, &current_id) && current_pid == pid && current_id == window_id) {
			found = 1;
			break;
		}
	}
	CFRelease(list);
	return found;
}

// Match a public AX window to the selected CG window by process and unique
// geometry. Ambiguous matches fail closed rather than choosing a different app
// window. The returned reference is retained and must be released.
static AXUIElementRef cu_ax_window(cu_window selected, int require_focus) {
	if (!AXIsProcessTrusted() || !cu_window_exists(selected.window_id, selected.pid, selected.start_sec, selected.start_usec)) return NULL;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, selected.window_id);
	if (!list || CFArrayGetCount(list) != 1) { if (list) CFRelease(list); return NULL; }
	CFDictionaryRef dict = (CFDictionaryRef)CFArrayGetValueAtIndex(list, 0);
	CFDictionaryRef bounds_dict = (CFDictionaryRef)CFDictionaryGetValue(dict, kCGWindowBounds);
	CGRect bounds;
	int have_bounds = bounds_dict && CGRectMakeWithDictionaryRepresentation(bounds_dict, &bounds);
	CFRelease(list);
	if (!have_bounds) return NULL;
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	if (!app) return NULL;
	AXUIElementSetMessagingTimeout(app, 0.5);
	CFTypeRef raw = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &raw) != kAXErrorSuccess || !raw || CFGetTypeID(raw) != CFArrayGetTypeID()) {
		if (raw) CFRelease(raw);
		CFRelease(app);
		return NULL;
	}
	CFArrayRef windows = (CFArrayRef)raw;
	AXUIElementRef match = NULL;
	int matches = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(windows) && i < 128; i++) {
		AXUIElementRef window = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CFTypeRef position = NULL, size = NULL;
		CGPoint point;
		CGSize dimensions;
		AXUIElementSetMessagingTimeout(window, 0.5);
		int ok = AXUIElementCopyAttributeValue(window, kAXPositionAttribute, &position) == kAXErrorSuccess &&
			AXUIElementCopyAttributeValue(window, kAXSizeAttribute, &size) == kAXErrorSuccess &&
			position && size && CFGetTypeID(position) == AXValueGetTypeID() && CFGetTypeID(size) == AXValueGetTypeID() &&
			AXValueGetValue((AXValueRef)position, kAXValueCGPointType, &point) &&
			AXValueGetValue((AXValueRef)size, kAXValueCGSizeType, &dimensions);
		if (position) CFRelease(position);
		if (size) CFRelease(size);
		if (ok && fabs(point.x-bounds.origin.x) < 2 && fabs(point.y-bounds.origin.y) < 2 &&
			fabs(dimensions.width-bounds.size.width) < 2 && fabs(dimensions.height-bounds.size.height) < 2) {
			match = window;
			matches++;
		}
	}
	if (matches != 1) match = NULL;
	if (match && require_focus) {
		AXUIElementRef system = AXUIElementCreateSystemWide();
		CFTypeRef focused_app = NULL, focused_window = NULL;
		int ok = system && AXUIElementCopyAttributeValue(system, kAXFocusedApplicationAttribute, &focused_app) == kAXErrorSuccess &&
			focused_app && CFEqual(focused_app, app) &&
			AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, &focused_window) == kAXErrorSuccess &&
			focused_window && CFEqual(focused_window, match);
		if (focused_app) CFRelease(focused_app);
		if (focused_window) CFRelease(focused_window);
		if (system) CFRelease(system);
		if (!ok) match = NULL;
	}
	if (match) CFRetain(match);
	CFRelease(windows);
	CFRelease(app);
	return match;
}

typedef struct {
	AXUIElementRef element;
	char role[128];
	char name[512];
	char value[512];
	int enabled;
	int editable;
	int secure;
} cu_ax_node;

static int cu_ax_text_role(CFTypeRef role) {
	return role && CFGetTypeID(role) == CFStringGetTypeID() &&
		(CFEqual(role, CFSTR("AXTextField")) || CFEqual(role, CFSTR("AXTextArea")));
}

// A text control without a readable subrole cannot be distinguished from a
// secure field, and an element without a readable role cannot be classified.
static int cu_ax_secure_or_unknown(CFTypeRef role, CFTypeRef subrole) {
	if (!role || CFGetTypeID(role) != CFStringGetTypeID()) return 1;
	if (CFEqual(role, CFSTR("AXSecureTextField"))) return 1;
	if (subrole && CFGetTypeID(subrole) == CFStringGetTypeID()) {
		return CFEqual(subrole, CFSTR("AXSecureTextField"));
	}
	return cu_ax_text_role(role);
}

static void cu_ax_collect(AXUIElementRef element, cu_ax_node *nodes, int cap, int *count, int depth, int ancestor_secure, int *truncated) {
	if (!element || *count >= cap || depth > 16) { *truncated = 1; return; }
	AXUIElementSetMessagingTimeout(element, 0.3);
	CFTypeRef role = NULL, subrole = NULL, title = NULL, enabled = NULL, value = NULL;
	AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role);
	AXUIElementCopyAttributeValue(element, kAXSubroleAttribute, &subrole);
	int secure = ancestor_secure || cu_ax_secure_or_unknown(role, subrole);
	cu_ax_node *node = &nodes[(*count)++];
	node->element = (AXUIElementRef)CFRetain(element);
	node->secure = secure;
	cu_string(role, node->role, sizeof(node->role));
	if (!secure) {
		if (AXUIElementCopyAttributeValue(element, kAXTitleAttribute, &title) != kAXErrorSuccess || !title) {
			if (title) CFRelease(title);
			title = NULL;
			AXUIElementCopyAttributeValue(element, kAXDescriptionAttribute, &title);
		}
		cu_string(title, node->name, sizeof(node->name));
		// Only static text is read; editable and secure control values are not.
		if (role && CFGetTypeID(role) == CFStringGetTypeID() && CFEqual(role, CFSTR("AXStaticText"))) {
			AXUIElementCopyAttributeValue(element, kAXValueAttribute, &value);
			cu_string(value, node->value, sizeof(node->value));
		}
		Boolean settable = false;
		if (cu_ax_text_role(role) && AXUIElementIsAttributeSettable(element, kAXValueAttribute, &settable) == kAXErrorSuccess && settable) {
			node->editable = 1;
		}
	}
	if (AXUIElementCopyAttributeValue(element, kAXEnabledAttribute, &enabled) == kAXErrorSuccess && enabled && CFGetTypeID(enabled) == CFBooleanGetTypeID()) {
		node->enabled = CFBooleanGetValue((CFBooleanRef)enabled);
	}
	if (role) CFRelease(role);
	if (subrole) CFRelease(subrole);
	if (title) CFRelease(title);
	if (enabled) CFRelease(enabled);
	if (value) CFRelease(value);
	if (secure) return;
	CFTypeRef children = NULL;
	if (AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, &children) == kAXErrorSuccess && children && CFGetTypeID(children) == CFArrayGetTypeID()) {
		CFArrayRef array = (CFArrayRef)children;
		for (CFIndex i = 0; i < CFArrayGetCount(array); i++) {
			if (*count >= cap) { *truncated = 1; break; }
			CFTypeRef child = CFArrayGetValueAtIndex(array, i);
			if (child && CFGetTypeID(child) == AXUIElementGetTypeID()) cu_ax_collect((AXUIElementRef)child, nodes, cap, count, depth+1, secure, truncated);
		}
	}
	if (children) CFRelease(children);
}

static int cu_ax_collect_window(cu_window selected, AXUIElementRef expected, cu_ax_node *nodes, int cap, int *truncated) {
	AXUIElementRef window = cu_ax_window(selected, 0);
	if (!window || !expected || !CFEqual(window, expected)) { if (window) CFRelease(window); return -1; }
	int count = 0;
	cu_ax_collect(window, nodes, cap, &count, 0, 0, truncated);
	CFRelease(window);
	return count;
}

static void cu_ax_release(AXUIElementRef element) { if (element) CFRelease(element); }
static int cu_ax_equal(AXUIElementRef a, AXUIElementRef b) { return a && b && CFEqual(a, b); }

typedef struct { atomic_int cancelled; } cu_cancel_flag;
static cu_cancel_flag *cu_cancel_new(void) { return calloc(1, sizeof(cu_cancel_flag)); }
static void cu_cancel_set(cu_cancel_flag *flag) { atomic_store(&flag->cancelled, 1); }
static int cu_cancelled(cu_cancel_flag *flag) { return atomic_load(&flag->cancelled); }

static int cu_ax_contains(AXUIElementRef element, AXUIElementRef wanted, int depth, int *budget, cu_cancel_flag *flag) {
	if (!element || depth > 16 || *budget <= 0 || cu_cancelled(flag)) return 0;
	(*budget)--;
	AXUIElementSetMessagingTimeout(element, 0.3);
	CFTypeRef role = NULL, subrole = NULL;
	AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role);
	AXUIElementCopyAttributeValue(element, kAXSubroleAttribute, &subrole);
	int secure = cu_ax_secure_or_unknown(role, subrole);
	if (role) CFRelease(role);
	if (subrole) CFRelease(subrole);
	if (secure) return 0;
	if (CFEqual(element, wanted)) return 1;
	CFTypeRef children = NULL;
	int found = 0;
	if (AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, &children) == kAXErrorSuccess && children && CFGetTypeID(children) == CFArrayGetTypeID()) {
		CFArrayRef array = (CFArrayRef)children;
		for (CFIndex i = 0; i < CFArrayGetCount(array) && *budget > 0 && !found; i++) {
			CFTypeRef child = CFArrayGetValueAtIndex(array, i);
			if (child && CFGetTypeID(child) == AXUIElementGetTypeID()) found = cu_ax_contains((AXUIElementRef)child, wanted, depth+1, budget, flag);
		}
	}
	if (children) CFRelease(children);
	return found;
}

static int cu_ax_press(cu_window selected, AXUIElementRef expected, AXUIElementRef element, cu_cancel_flag *flag) {
	if (!expected || !element || cu_cancelled(flag)) return 0;
	// AXPress is addressed to the pinned element, not to the foreground app.
	// Requiring focus would make an operator's own terminal pane steal focus
	// from the target. Physical keyboard/mouse injection still requires focus.
	AXUIElementRef window = cu_ax_window(selected, 0);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int budget = 128;
	int found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) return 0;
	CFTypeRef enabled = NULL;
	int allowed = AXUIElementCopyAttributeValue(element, kAXEnabledAttribute, &enabled) == kAXErrorSuccess && enabled &&
		CFGetTypeID(enabled) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)enabled);
	if (enabled) CFRelease(enabled);
	if (!allowed || cu_cancelled(flag)) return 0;
	CFArrayRef actions = NULL;
	allowed = AXUIElementCopyActionNames(element, &actions) == kAXErrorSuccess && actions &&
		CFArrayContainsValue(actions, CFRangeMake(0, CFArrayGetCount(actions)), kAXPressAction);
	if (actions) CFRelease(actions);
	if (!allowed || cu_cancelled(flag)) return 0;
	window = cu_ax_window(selected, 0);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	budget = 128;
	found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) return 0;
	return AXUIElementPerformAction(element, kAXPressAction) == kAXErrorSuccess;
}

static int cu_ax_fill(cu_window selected, AXUIElementRef expected, AXUIElementRef element, const char *text, size_t length, cu_cancel_flag *flag) {
	if (!expected || !element || !text || length > 4096 || cu_cancelled(flag)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 0);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int budget = 128;
	int found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) return 0;
	CFTypeRef role = NULL, enabled = NULL;
	Boolean settable = false;
	int allowed = AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role) == kAXErrorSuccess && cu_ax_text_role(role) &&
		AXUIElementCopyAttributeValue(element, kAXEnabledAttribute, &enabled) == kAXErrorSuccess && enabled &&
		CFGetTypeID(enabled) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)enabled) &&
		AXUIElementIsAttributeSettable(element, kAXValueAttribute, &settable) == kAXErrorSuccess && settable;
	if (role) CFRelease(role);
	if (enabled) CFRelease(enabled);
	if (!allowed || cu_cancelled(flag)) return 0;
	CFStringRef value = CFStringCreateWithBytes(kCFAllocatorDefault, (const UInt8 *)text, (CFIndex)length, kCFStringEncodingUTF8, false);
	if (!value) return 0;
	window = cu_ax_window(selected, 0);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); CFRelease(value); return 0; }
	budget = 128;
	found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) { CFRelease(value); return 0; }
	AXError result = AXUIElementSetAttributeValue(element, kAXValueAttribute, value);
	CFRelease(value);
	return result == kAXErrorSuccess;
}
*/
import "C"

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

// MacWindowCatalog is a non-prompting operator-facing window inventory and AX
// adapter. It does not implement coordinate input.
type MacWindowCatalog struct {
	mu          sync.Mutex
	windows     map[string]C.cu_window
	pinned      map[string]C.AXUIElementRef
	refs        map[string]C.AXUIElementRef
	snapshotID  string
	snapshotFor string
	closed      bool
}

func NewMacWindowCatalog() *MacWindowCatalog {
	return &MacWindowCatalog{windows: make(map[string]C.cu_window), pinned: make(map[string]C.AXUIElementRef)}
}

// Targets must only be called by the operator-facing target picker, not by a
// model-facing list action. The opaque identifiers are local to this catalog.
func (c *MacWindowCatalog) Targets(ctx context.Context) ([]Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var windows [128]C.cu_window
	n := int(C.cu_windows(&windows[0], C.int(len(windows))))
	if n < 0 {
		return nil, errors.New("macOS window inventory unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Target, 0, n)
	next := make(map[string]C.cu_window, n)
	for _, window := range windows[:n] {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(nonce[:])
		next[id] = window
		out = append(out, Target{ID: id, Application: C.GoString(&window.application[0]), Window: C.GoString(&window.title[0])})
	}
	for _, ref := range c.pinned {
		C.cu_ax_release(ref)
	}
	c.pinned = make(map[string]C.AXUIElementRef)
	c.clearSnapshotLocked()
	c.windows = next
	return out, nil
}

// The current macOS control backend offers only target-addressed AX actions.
// Future physical input must independently prove foreground focus and geometry
// immediately before injection; this identity check does not grant that right.
func (c *MacWindowCatalog) Validate(ctx context.Context, target Target, _ bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok || target.Application != C.GoString(&window.application[0]) || target.Window != C.GoString(&window.title[0]) {
		return ErrTargetLost
	}
	if C.cu_window_exists(window.window_id, window.pid, window.start_sec, window.start_usec) == 0 {
		return ErrTargetLost
	}
	current := C.cu_ax_window(window, 0)
	if current == 0 {
		return errors.New("macOS AX window unavailable, ambiguous, or not authorized")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		C.cu_ax_release(current)
		return err
	}
	latest, exists := c.windows[target.ID]
	if c.closed || !exists || latest.window_id != window.window_id || latest.pid != window.pid || latest.start_sec != window.start_sec || latest.start_usec != window.start_usec {
		C.cu_ax_release(current)
		return ErrTargetLost
	}
	if pinned := c.pinned[target.ID]; pinned != 0 {
		matches := C.cu_ax_equal(pinned, current) != 0
		C.cu_ax_release(current)
		if !matches {
			return ErrTargetLost
		}
	} else {
		c.pinned[target.ID] = current
	}
	return nil
}

func (c *MacWindowCatalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.clearSnapshotLocked()
	for _, ref := range c.pinned {
		C.cu_ax_release(ref)
	}
	c.pinned = nil
	c.windows = nil
	return nil
}

func (c *MacWindowCatalog) clearSnapshotLocked() {
	for _, ref := range c.refs {
		C.cu_ax_release(ref)
	}
	c.refs, c.snapshotID, c.snapshotFor = nil, "", ""
}

// Snapshot obtains bounded AX metadata for a uniquely matched selected window.
// It reads AXValue only for non-secure static text, never editable fields.
func (c *MacWindowCatalog) Snapshot(ctx context.Context, target Target) (string, []Element, bool, error) {
	if err := c.Validate(ctx, target, false); err != nil {
		return "", nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok {
		return "", nil, false, ErrTargetLost
	}
	c.clearSnapshotLocked()
	const cap = 128
	buffer := (*C.cu_ax_node)(C.calloc(cap, C.size_t(C.sizeof_cu_ax_node)))
	if buffer == nil {
		return "", nil, false, errors.New("allocate AX snapshot")
	}
	defer C.free(unsafe.Pointer(buffer))
	var truncated C.int
	count := int(C.cu_ax_collect_window(window, c.pinned[target.ID], buffer, cap, &truncated))
	if count < 0 {
		return "", nil, false, fmt.Errorf("%w: macOS AX window unavailable, ambiguous, or not authorized", ErrTargetLost)
	}
	nodes := unsafe.Slice(buffer, count)
	refs := make(map[string]C.AXUIElementRef, count)
	elements := make([]Element, 0, count)
	for i := range nodes {
		ref := fmt.Sprintf("@e%d", i+1)
		refs[ref] = nodes[i].element
		elements = append(elements, Element{Ref: ref, Role: C.GoString(&nodes[i].role[0]), Name: C.GoString(&nodes[i].name[0]), Value: C.GoString(&nodes[i].value[0]), Enabled: nodes[i].enabled != 0, Editable: nodes[i].editable != 0, Secure: nodes[i].secure != 0})
	}
	if err := ctx.Err(); err != nil {
		for _, ref := range refs {
			C.cu_ax_release(ref)
		}
		return "", nil, false, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		for _, ref := range refs {
			C.cu_ax_release(ref)
		}
		return "", nil, false, err
	}
	c.refs, c.snapshotID, c.snapshotFor = refs, hex.EncodeToString(nonce[:]), target.ID
	return c.snapshotID, elements, truncated != 0, nil
}

func (*MacWindowCatalog) SupportsAction(action string) bool {
	return action == "click" || action == "fill"
}

// Act executes a targeted AXPress or non-secure AXValue replacement on a node
// from the bound backend snapshot. Target and membership are rechecked before
// the OS request; cancellation cannot undo an already-sent action.
func (c *MacWindowCatalog) Act(ctx context.Context, target Target, snapshotID, ref, action, text string) error {
	if (action != "click" && action != "fill") || (action == "click" && text != "") || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return ErrNotAllowed
	}
	if err := c.Validate(ctx, target, true); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	window, ok := c.windows[target.ID]
	element, found := c.refs[ref]
	if c.closed || !ok || !found || c.snapshotID != snapshotID || c.snapshotFor != target.ID {
		return ErrStaleObservation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		return errors.New("allocate AX cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	var performed C.int
	if action == "click" {
		performed = C.cu_ax_press(window, c.pinned[target.ID], element, flag)
	} else {
		value := C.CString(text)
		performed = C.cu_ax_fill(window, c.pinned[target.ID], element, value, C.size_t(len(text)), flag)
		C.free(unsafe.Pointer(value))
	}
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.clearSnapshotLocked()
	if err := ctx.Err(); err != nil {
		return err
	}
	if performed == 0 {
		return fmt.Errorf("%w: AX action refused because the target changed, the node is secure or the action is unavailable", ErrTargetLost)
	}
	return nil
}

// Capture uses ScreenCaptureKit for the selected on-screen window only. It
// checks the pinned window before and after capture; no system prompt is raised.
func (c *MacWindowCatalog) Capture(ctx context.Context, target Target) ([]byte, error) {
	if err := c.Validate(ctx, target, false); err != nil {
		return nil, err
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok {
		return nil, ErrTargetLost
	}
	var data unsafe.Pointer
	var length C.size_t
	if C.cu_capture_window(C.uint32_t(window.pid), window.window_id, &data, &length) == 0 {
		return nil, errors.New("ScreenCaptureKit capture unavailable or not authorized")
	}
	defer C.free(data)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if length == 0 || length > MaxFrameBytes {
		return nil, errors.New("ScreenCaptureKit frame exceeds the size limit")
	}
	if err := c.Validate(ctx, target, false); err != nil {
		return nil, err
	}
	return C.GoBytes(data, C.int(length)), nil
}
