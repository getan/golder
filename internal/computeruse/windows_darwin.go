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

int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length, double *x, double *y, double *w, double *h);
int cu_app_allowed(int32_t pid);
int cu_running_bundle(int32_t pid, char *out, size_t length);

typedef struct {
	uint32_t window_id;
	int32_t pid;
	uint64_t start_sec;
	uint64_t start_usec;
	int on_screen;
	char application[256];
	char bundle[4096];
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
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
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
		CFTypeRef on_screen = CFDictionaryGetValue(d, kCGWindowIsOnscreen);
		entry.on_screen = on_screen && CFGetTypeID(on_screen) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)on_screen);
		if (!cu_process_start(pid, &entry.start_sec, &entry.start_usec)) continue;
		if (!cu_running_bundle(pid, entry.bundle, sizeof(entry.bundle))) continue;
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
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, window_id);
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

enum {
	CU_AX_OK = 0, CU_AX_PERMISSION, CU_AX_WINDOW, CU_AX_BOUNDS, CU_AX_APP,
	CU_AX_READ, CU_AX_EMPTY, CU_AX_GEOMETRY, CU_AX_NO_MATCH, CU_AX_AMBIGUOUS,
	CU_AX_LIMIT, CU_AX_FOCUS, CU_AX_DISABLED
};

typedef struct {
	int reason;
	int ax_error;
	int windows;
	int readable;
	int matches;
} cu_ax_diagnostic;

static int cu_ax_match_reason(CFIndex windows, int readable, int matches) {
	if (windows > 128) return CU_AX_LIMIT;
	if (matches > 1) return CU_AX_AMBIGUOUS;
	if (windows == 0) return CU_AX_EMPTY;
	if (readable < windows) return CU_AX_GEOMETRY;
	if (matches == 1) return CU_AX_OK;
	return CU_AX_NO_MATCH;
}

// Match a public AX window to the selected CG window by process and unique
// geometry. The optional diagnostic contains only failure stages and counts,
// never element content. Input checks use the same matching path.
static AXUIElementRef cu_ax_window_diagnostic(cu_window selected, int require_focus, cu_ax_diagnostic *diag) {
	cu_ax_diagnostic result = {0};
	if (!diag) diag = &result;
	*diag = result;
	if (!AXIsProcessTrusted()) { diag->reason = CU_AX_PERMISSION; return NULL; }
	if (!cu_window_exists(selected.window_id, selected.pid, selected.start_sec, selected.start_usec)) { diag->reason = CU_AX_WINDOW; return NULL; }
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, selected.window_id);
	if (!list || CFArrayGetCount(list) != 1) { if (list) CFRelease(list); diag->reason = CU_AX_WINDOW; return NULL; }
	CFDictionaryRef dict = (CFDictionaryRef)CFArrayGetValueAtIndex(list, 0);
	CFDictionaryRef bounds_dict = (CFDictionaryRef)CFDictionaryGetValue(dict, kCGWindowBounds);
	CGRect bounds;
	int have_bounds = bounds_dict && CGRectMakeWithDictionaryRepresentation(bounds_dict, &bounds);
	CFRelease(list);
	if (!have_bounds) { diag->reason = CU_AX_BOUNDS; return NULL; }
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	if (!app) { diag->reason = CU_AX_APP; return NULL; }
	AXUIElementSetMessagingTimeout(app, 0.5);
	CFTypeRef raw = NULL;
	AXError ax_error = AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &raw);
	if (ax_error != kAXErrorSuccess || !raw || CFGetTypeID(raw) != CFArrayGetTypeID()) {
		if (raw) CFRelease(raw);
		CFRelease(app);
		diag->reason = ax_error == kAXErrorAPIDisabled ? CU_AX_DISABLED :
			(ax_error == kAXErrorNoValue || (ax_error == kAXErrorSuccess && !raw) ? CU_AX_EMPTY : CU_AX_READ);
		diag->ax_error = (int)ax_error;
		return NULL;
	}
	CFArrayRef windows = (CFArrayRef)raw;
	AXUIElementRef match = NULL;
	CFIndex total = CFArrayGetCount(windows);
	diag->windows = total > INT32_MAX ? INT32_MAX : (int)total;
	int matches = 0, readable = 0;
	for (CFIndex i = 0; i < total && i < 128; i++) {
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
		if (ok) readable++;
		if (ok && fabs(point.x-bounds.origin.x) < 2 && fabs(point.y-bounds.origin.y) < 2 &&
			fabs(dimensions.width-bounds.size.width) < 2 && fabs(dimensions.height-bounds.size.height) < 2) {
			match = window;
			matches++;
		}
	}
	diag->readable = readable;
	diag->matches = matches;
	diag->reason = cu_ax_match_reason(total, readable, matches);
	if (diag->reason != CU_AX_OK) match = NULL;
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
		if (!ok) { diag->reason = CU_AX_FOCUS; match = NULL; }
	}
	if (match) CFRetain(match);
	CFRelease(windows);
	CFRelease(app);
	return match;
}

static AXUIElementRef cu_ax_window(cu_window selected, int require_focus) {
	return cu_ax_window_diagnostic(selected, require_focus, NULL);
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

static int cu_ax_collect_window(cu_window selected, AXUIElementRef expected, cu_ax_node *nodes, int cap, int *truncated, cu_ax_diagnostic *diag) {
	AXUIElementRef window = cu_ax_window_diagnostic(selected, 0, diag);
	if (!window) return -1;
	if (!expected || !CFEqual(window, expected)) { CFRelease(window); diag->reason = CU_AX_WINDOW; return -1; }
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

static int cu_current_bounds(cu_window selected, CGRect *bounds) {
	if (!cu_window_exists(selected.window_id, selected.pid, selected.start_sec, selected.start_usec)) return 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, selected.window_id);
	if (!list || CFArrayGetCount(list) != 1) { if (list) CFRelease(list); return 0; }
	CFDictionaryRef item = (CFDictionaryRef)CFArrayGetValueAtIndex(list, 0);
	CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(item, kCGWindowBounds);
	int ok = raw && CGRectMakeWithDictionaryRepresentation(raw, bounds) && bounds->size.width > 0 && bounds->size.height > 0;
	CFRelease(list);
	return ok;
}

// Every window above the target must be known not to cover the requested point.
static int cu_point_unobscured(cu_window selected, CGPoint point) {
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return 0;
	int found = 0, clear = 1;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef item = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t number = 0;
		int32_t layer = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowNumber);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowLayer);
		if (!n || !l || !CFNumberGetValue(n, kCFNumberSInt32Type, &number) || !CFNumberGetValue(l, kCFNumberSInt32Type, &layer)) { clear = 0; break; }
		if (number == selected.window_id) { found = 1; break; }
		if (layer < 0) continue;
		CGRect bounds;
		CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(item, kCGWindowBounds);
		if (!raw || !CGRectMakeWithDictionaryRepresentation(raw, &bounds)) { clear = 0; break; }
		double alpha = 1;
		CFNumberRef a = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowAlpha);
		if ((a && !CFNumberGetValue(a, kCFNumberDoubleType, &alpha)) || !isfinite(alpha) || alpha < 0 || alpha > 1) { clear = 0; break; }
		if (alpha > 0 && CGRectContainsPoint(bounds, point)) { clear = 0; break; }
	}
	CFRelease(list);
	return found && clear;
}

// A container hit is ambiguous: it may hide a protected descendant that the
// target application did not expose. Never inject into an ambiguous container.
static int cu_direct_control(AXUIElementRef element) {
	CFTypeRef role = NULL;
	if (!element || AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role) != kAXErrorSuccess || !role || CFGetTypeID(role) != CFStringGetTypeID()) {
		if (role) CFRelease(role);
		return 0;
	}
	int direct = CFEqual(role, CFSTR("AXButton")) || CFEqual(role, CFSTR("AXCheckBox")) || CFEqual(role, CFSTR("AXRadioButton")) ||
		CFEqual(role, CFSTR("AXTextField")) || CFEqual(role, CFSTR("AXTextArea")) || CFEqual(role, CFSTR("AXSlider")) ||
		CFEqual(role, CFSTR("AXScrollBar")) || CFEqual(role, CFSTR("AXPopUpButton")) || CFEqual(role, CFSTR("AXMenuItem")) ||
		CFEqual(role, CFSTR("AXLink")) || CFEqual(role, CFSTR("AXImage")) || CFEqual(role, CFSTR("AXStaticText")) ||
		CFEqual(role, CFSTR("AXIncrementor")) || CFEqual(role, CFSTR("AXDisclosureTriangle"));
	CFRelease(role);
	return direct;
}

static int cu_point_ax_safe(AXUIElementRef window, CGPoint point, cu_cancel_flag *flag) {
	AXUIElementRef system = AXUIElementCreateSystemWide();
	AXUIElementRef hit = NULL;
	int ok = system && AXUIElementCopyElementAtPosition(system, point.x, point.y, &hit) == kAXErrorSuccess && hit && !CFEqual(window, hit) && cu_direct_control(hit);
	int budget = 128;
	if (ok) ok = cu_ax_contains(window, hit, 0, &budget, flag);
	if (hit) CFRelease(hit);
	if (system) CFRelease(system);
	return ok;
}

static int cu_frame_point_safe(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, cu_cancel_flag *flag, CGPoint *out) {
	if (!expected || cu_cancelled(flag) || !isfinite(bx) || !isfinite(by) || !isfinite(bw) || !isfinite(bh) || !isfinite(nx) || !isfinite(ny) || bw <= 0 || bh <= 0 || nx < 0 || nx >= 1 || ny < 0 || ny >= 1) return 0;
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5) return 0;
	CGPoint point = CGPointMake(bx + nx*bw, by + ny*bh);
	if (!cu_point_unobscured(selected, point)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 1);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int ok = cu_point_ax_safe(window, point, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5 || !cu_point_unobscured(selected, point)) return 0;
	window = cu_ax_window(selected, 1);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	ok = cu_point_ax_safe(window, point, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5 || !cu_point_unobscured(selected, point)) return 0;
	window = cu_ax_window(selected, 1);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	CFRelease(window);
	if (cu_cancelled(flag)) return 0;
	*out = point;
	return 1;
}

static int cu_click_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, cu_cancel_flag *flag) {
	CGPoint point;
	if (!cu_frame_point_safe(selected, expected, bx, by, bw, bh, nx, ny, flag, &point)) return 0;
	CGEventRef down = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseDown, point, kCGMouseButtonLeft);
	CGEventRef up = CGEventCreateMouseEvent(NULL, kCGEventLeftMouseUp, point, kCGMouseButtonLeft);
	if (!down || !up || cu_cancelled(flag)) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
	CGEventSetIntegerValueField(down, kCGMouseEventClickState, 1);
	CGEventSetIntegerValueField(up, kCGMouseEventClickState, 1);
	CGEventSetFlags(down, 0);
	CGEventSetFlags(up, 0);
	CGEventPostToPid(selected.pid, down);
	CGEventPostToPid(selected.pid, up);
	CFRelease(down);
	CFRelease(up);
	return 1; // dispatched, not proof that the app acted on the event
}

static int cu_scroll_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, int dx, int dy, cu_cancel_flag *flag) {
	if (dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0)) return 0;
	CGPoint point;
	if (!cu_frame_point_safe(selected, expected, bx, by, bw, bh, nx, ny, flag, &point)) return 0;
	CGEventRef event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 2, dy, dx);
	if (!event || cu_cancelled(flag)) { if (event) CFRelease(event); return 0; }
	CGEventSetLocation(event, point);
	CGEventSetFlags(event, 0);
	CGEventPostToPid(selected.pid, event);
	CFRelease(event);
	return 1;
}

static int cu_focused_control_safe(cu_window selected, AXUIElementRef window, cu_cancel_flag *flag) {
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	CFTypeRef focused = NULL;
	int ok = app && AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &focused) == kAXErrorSuccess && focused &&
		CFGetTypeID(focused) == AXUIElementGetTypeID() && !CFEqual(window, focused) && cu_direct_control((AXUIElementRef)focused);
	int budget = 128;
	if (ok) ok = cu_ax_contains(window, (AXUIElementRef)focused, 0, &budget, flag);
	CFTypeRef position = NULL, size = NULL;
	CGPoint point;
	CGSize dimensions;
	if (ok) {
		ok = AXUIElementCopyAttributeValue((AXUIElementRef)focused, kAXPositionAttribute, &position) == kAXErrorSuccess &&
			AXUIElementCopyAttributeValue((AXUIElementRef)focused, kAXSizeAttribute, &size) == kAXErrorSuccess && position && size &&
			CFGetTypeID(position) == AXValueGetTypeID() && CFGetTypeID(size) == AXValueGetTypeID() &&
			AXValueGetValue((AXValueRef)position, kAXValueCGPointType, &point) &&
			AXValueGetValue((AXValueRef)size, kAXValueCGSizeType, &dimensions) && dimensions.width > 0 && dimensions.height > 0;
	}
	if (ok) {
		point.x += dimensions.width / 2;
		point.y += dimensions.height / 2;
		ok = cu_point_unobscured(selected, point);
	}
	if (position) CFRelease(position);
	if (size) CFRelease(size);
	if (focused) CFRelease(focused);
	if (app) CFRelease(app);
	return ok;
}

static int cu_press_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, uint16_t code, cu_cancel_flag *flag) {
	if (!expected || cu_cancelled(flag) || !isfinite(bx) || !isfinite(by) || !isfinite(bw) || !isfinite(bh) || bw <= 0 || bh <= 0) return 0;
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5) return 0;
	AXUIElementRef window = cu_ax_window(selected, 1);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int ok = cu_focused_control_safe(selected, window, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag)) return 0;
	window = cu_ax_window(selected, 1);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	ok = cu_focused_control_safe(selected, window, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5) return 0;
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, code, true);
	CGEventRef up = CGEventCreateKeyboardEvent(NULL, code, false);
	if (!down || !up || cu_cancelled(flag)) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
	CGEventSetFlags(down, 0);
	CGEventSetFlags(up, 0);
	CGEventPostToPid(selected.pid, down);
	CGEventPostToPid(selected.pid, up);
	CFRelease(down);
	CFRelease(up);
	return 1;
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

static int cu_ax_close_window(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag) {
	if (!expected || cu_cancelled(flag)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 0);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	CFTypeRef button = NULL;
	AXError error = AXUIElementCopyAttributeValue(window, kAXCloseButtonAttribute, &button);
	CFRelease(window);
	if (error != kAXErrorSuccess || !button || CFGetTypeID(button) != AXUIElementGetTypeID()) {
		if (button) CFRelease(button);
		return 0;
	}
	int sent = cu_ax_press(selected, expected, (AXUIElementRef)button, flag);
	CFRelease(button);
	return sent;
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

// MacWindowCatalog is a non-prompting operator-facing window inventory and AX
// adapter with frame-bound, fail-closed coordinate input.
type MacWindowCatalog struct {
	mu           sync.Mutex
	windows      map[string]C.cu_window
	applications map[string]macApplication
	pinned       map[string]C.AXUIElementRef
	refs         map[string]C.AXUIElementRef
	snapshotID   string
	snapshotFor  string
	closed       bool
}

func NewMacWindowCatalog() *MacWindowCatalog {
	return &MacWindowCatalog{windows: make(map[string]C.cu_window), pinned: make(map[string]C.AXUIElementRef)}
}

// Targets is called by local target resolution, never by a model-facing list
// action. The opaque identifiers are local to this catalog.
func (c *MacWindowCatalog) Targets(ctx context.Context) ([]Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var windows [128]C.cu_window
	n := int(C.cu_windows(&windows[0], C.int(len(windows))))
	if n < 0 {
		return nil, errors.New("macOS window inventory unavailable")
	}
	if n == len(windows) {
		return nil, errors.New("macOS window inventory exceeded its safe limit")
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
		detail := fmt.Sprintf("PID %d · Window %d", int32(window.pid), uint32(window.window_id))
		if window.on_screen == 0 {
			detail += " · Off-screen"
		}
		out = append(out, Target{ID: id, Application: C.GoString(&window.application[0]), ApplicationID: C.GoString(&window.bundle[0]), Window: C.GoString(&window.title[0]), Detail: detail})
	}
	for _, ref := range c.pinned {
		C.cu_ax_release(ref)
	}
	c.pinned = make(map[string]C.AXUIElementRef)
	c.clearSnapshotLocked()
	c.windows = next
	return out, nil
}

func macWindowDiagnostic(d C.cu_ax_diagnostic) *WindowDiagnostic {
	reasons := map[int]WindowFailure{
		C.CU_AX_PERMISSION: WindowPermissionDenied, C.CU_AX_WINDOW: WindowUnavailable,
		C.CU_AX_BOUNDS: WindowBoundsMissing, C.CU_AX_APP: WindowAXAppUnavailable,
		C.CU_AX_READ: WindowAXReadFailed, C.CU_AX_EMPTY: WindowAXNoWindows,
		C.CU_AX_GEOMETRY: WindowAXGeometry, C.CU_AX_NO_MATCH: WindowAXNoMatch,
		C.CU_AX_AMBIGUOUS: WindowAXAmbiguous, C.CU_AX_LIMIT: WindowAXLimit,
		C.CU_AX_FOCUS: WindowAXFocus, C.CU_AX_DISABLED: WindowAXDisabled,
	}
	return &WindowDiagnostic{Reason: reasons[int(d.reason)], AXError: int(d.ax_error),
		Windows: int(d.windows), Readable: int(d.readable), Matches: int(d.matches)}
}

// macAXMatchReason exposes the native match decision to deterministic tests
// without opening or controlling an application window.
func macAXMatchReason(windows, readable, matches int) WindowFailure {
	reason := C.cu_ax_match_reason(C.CFIndex(windows), C.int(readable), C.int(matches))
	return macWindowDiagnostic(C.cu_ax_diagnostic{reason: reason}).Reason
}

// Physical input independently proves foreground focus and geometry immediately
// before injection; this identity check alone does not grant that right.
func (c *MacWindowCatalog) Validate(ctx context.Context, target Target, _ bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok || target.Application != C.GoString(&window.application[0]) || target.ApplicationID != C.GoString(&window.bundle[0]) || target.Window != C.GoString(&window.title[0]) {
		return &WindowDiagnostic{Reason: WindowUnavailable}
	}
	if C.cu_window_exists(window.window_id, window.pid, window.start_sec, window.start_usec) == 0 {
		return &WindowDiagnostic{Reason: WindowUnavailable}
	}
	var diagnostic C.cu_ax_diagnostic
	current := C.cu_ax_window_diagnostic(window, 0, &diagnostic)
	if current == 0 {
		return macWindowDiagnostic(diagnostic)
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
	c.applications = nil
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
	var diagnostic C.cu_ax_diagnostic
	count := int(C.cu_ax_collect_window(window, c.pinned[target.ID], buffer, cap, &truncated, &diagnostic))
	if count < 0 {
		return "", nil, false, macWindowDiagnostic(diagnostic)
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

// CloseWindow sends a close request to the pinned window, never to its app.
func (c *MacWindowCatalog) CloseWindow(ctx context.Context, target Target) error {
	if err := c.Validate(ctx, target, true); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		return ErrTargetLost
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
	sent := C.cu_ax_close_window(window, c.pinned[target.ID], flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.clearSnapshotLocked()
	if err := ctx.Err(); err != nil {
		return err
	}
	if sent == 0 {
		return errors.New("close request unavailable or refused by the selected window")
	}
	return nil
}

// Capture uses ScreenCaptureKit for the selected window only. It
// checks the pinned window before and after capture; no system prompt is raised.
func (c *MacWindowCatalog) Capture(ctx context.Context, target Target) ([]byte, error) {
	data, _, err := c.CaptureBound(ctx, target)
	return data, err
}

type macFrameBinding struct {
	Target string
	X, Y   float64
	W, H   float64
	Digest [32]byte
}

func (c *MacWindowCatalog) CaptureBound(ctx context.Context, target Target) ([]byte, string, error) {
	if err := c.Validate(ctx, target, false); err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok {
		return nil, "", ErrTargetLost
	}
	var data unsafe.Pointer
	var length C.size_t
	var x, y, w, h C.double
	if C.cu_capture_window(C.uint32_t(window.pid), window.window_id, &data, &length, &x, &y, &w, &h) == 0 {
		return nil, "", errors.New("ScreenCaptureKit capture unavailable or not authorized")
	}
	defer C.free(data)
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if length == 0 || length > MaxFrameBytes {
		return nil, "", errors.New("ScreenCaptureKit frame exceeds the size limit")
	}
	if err := c.Validate(ctx, target, false); err != nil {
		return nil, "", err
	}
	image := C.GoBytes(data, C.int(length))
	binding, err := json.Marshal(macFrameBinding{Target: target.ID, X: float64(x), Y: float64(y), W: float64(w), H: float64(h), Digest: sha256.Sum256(image)})
	if err != nil {
		return nil, "", err
	}
	return image, string(binding), nil
}

// ClickFrame refuses unless the same window, frame geometry, focus, AX hit and
// absence of occluding windows can be established at the point of dispatch.
func (c *MacWindowCatalog) ClickFrame(ctx context.Context, target Target, token string, x, y float64) error {
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_click_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.double(x), C.double(y), flag)
	})
}

func (c *MacWindowCatalog) ScrollFrame(ctx context.Context, target Target, token string, x, y float64, dx, dy int) error {
	if dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
		return ErrNotAllowed
	}
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_scroll_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.double(x), C.double(y), C.int(dx), C.int(dy), flag)
	})
}

func (c *MacWindowCatalog) PressFrame(ctx context.Context, target Target, token, key string) error {
	codes := map[string]uint16{"enter": 36, "tab": 48, "escape": 53, "backspace": 51, "delete": 117, "left": 123, "right": 124, "down": 125, "up": 126, "page_up": 116, "page_down": 121, "home": 115, "end": 119, "space": 49}
	code, ok := codes[key]
	if !ok {
		return ErrNotAllowed
	}
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_press_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.uint16_t(code), flag)
	})
}

func (c *MacWindowCatalog) dispatchFrame(ctx context.Context, target Target, token string, dispatch func(C.cu_window, C.AXUIElementRef, macFrameBinding, *C.cu_cancel_flag) C.int) error {
	var binding macFrameBinding
	if err := json.Unmarshal([]byte(token), &binding); err != nil || binding.Target != target.ID {
		return ErrStaleObservation
	}
	fresh, freshToken, err := c.CaptureBound(ctx, target)
	if err != nil {
		return err
	}
	var current macFrameBinding
	if json.Unmarshal([]byte(freshToken), &current) != nil || current != binding || sha256.Sum256(fresh) != binding.Digest {
		return ErrStaleObservation
	}
	if err := c.Validate(ctx, target, true); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		return ErrTargetLost
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		return errors.New("allocate input cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	sent := dispatch(window, c.pinned[target.ID], binding, flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	if err := ctx.Err(); err != nil {
		return err
	}
	if sent == 0 {
		return fmt.Errorf("%w: input refused because target, geometry, focus, visibility or AX hit could not be verified", ErrTargetLost)
	}
	return nil
}
