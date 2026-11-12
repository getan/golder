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

int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length, double *x, double *y, double *w, double *h, void *cancel);
int cu_app_allowed(int32_t pid);
int cu_running_bundle(int32_t pid, char *out, size_t length);
int cu_activate_running_app(int32_t pid, const char *expectedPath);

typedef struct {
	uint32_t window_id;
	int32_t pid;
	uint64_t start_sec;
	uint64_t start_usec;
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

static int cu_window_on_screen(CFTypeRef value) {
	return value && CFGetTypeID(value) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)value);
}

static int cu_test_on_screen(int value) {
	if (value == 2) return cu_window_on_screen(CFSTR("invalid"));
	return cu_window_on_screen(value < 0 ? NULL : (value ? kCFBooleanTrue : kCFBooleanFalse));
}

static int cu_windows(cu_window *out, int cap, int all_spaces) {
	CGWindowListOption options = (all_spaces ? kCGWindowListOptionAll : kCGWindowListOptionOnScreenOnly) | kCGWindowListExcludeDesktopElements;
	CFArrayRef list = CGWindowListCopyWindowInfo(options, kCGNullWindowID);
	if (!list) return -1;
	int count = 0;
	CFIndex total = CFArrayGetCount(list);
	for (CFIndex i = 0; i < total; i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		if (!all_spaces && !cu_window_on_screen(CFDictionaryGetValue(d, kCGWindowIsOnscreen))) continue;
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
		if (!cu_running_bundle(pid, entry.bundle, sizeof(entry.bundle))) continue;
		cu_string(CFDictionaryGetValue(d, kCGWindowOwnerName), entry.application, sizeof(entry.application));
		cu_string(CFDictionaryGetValue(d, kCGWindowName), entry.title, sizeof(entry.title));
		if (entry.application[0] == 0) continue;
		if (count < cap) out[count] = entry;
		count++;
		// Keep this bound in sync with collectWindowInventory's maximum.
		if (count > 8192) break;
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

// A physical input operation may raise a window only while that exact window
// is already on the current desktop. Never activate another macOS Space merely
// to satisfy a frame-bound action.
static int cu_window_visible_here(cu_window selected) {
	if (!cu_window_exists(selected.window_id, selected.pid, selected.start_sec, selected.start_usec)) return 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return 0;
	int found = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t id = 0;
		int32_t pid = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		if (n && p && CFNumberGetValue(n, kCFNumberSInt32Type, &id) && CFNumberGetValue(p, kCFNumberSInt32Type, &pid) &&
			id == selected.window_id && pid == selected.pid && cu_window_on_screen(CFDictionaryGetValue(d, kCGWindowIsOnscreen))) {
			found = 1;
			break;
		}
	}
	CFRelease(list);
	return found;
}

// App activation can make macOS jump to the app's last-used Space instead of
// the intended window. Refuse to activate if another ordinary window of this
// process is off-screen or its visibility cannot be determined.
static int cu_activation_safe_here(cu_window selected) {
	if (!cu_window_visible_here(selected)) return 0;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return 0;
	int safe = 1;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t id = 0;
		int32_t pid = 0, layer = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		if (!p || !CFNumberGetValue(p, kCFNumberSInt32Type, &pid) || pid != selected.pid) continue;
		if (!n || !l || !CFNumberGetValue(n, kCFNumberSInt32Type, &id) || !CFNumberGetValue(l, kCFNumberSInt32Type, &layer)) { safe = 0; break; }
		if (id == selected.window_id || layer != 0) continue;
		CGRect bounds;
		CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		if (!raw || !CGRectMakeWithDictionaryRepresentation(raw, &bounds)) { safe = 0; break; }
		if (bounds.size.width >= 1 && bounds.size.height >= 1 && !cu_window_on_screen(CFDictionaryGetValue(d, kCGWindowIsOnscreen))) { safe = 0; break; }
	}
	CFRelease(list);
	return safe;
}

enum {
	CU_AX_OK = 0, CU_AX_PERMISSION, CU_AX_WINDOW, CU_AX_BOUNDS, CU_AX_APP,
	CU_AX_READ, CU_AX_EMPTY, CU_AX_GEOMETRY, CU_AX_NO_MATCH, CU_AX_AMBIGUOUS,
	CU_AX_LIMIT, CU_AX_FOCUS, CU_AX_DISABLED, CU_AX_NO_VALUE, CU_AX_NULL_VALUE,
	CU_AX_UNEXPECTED_TYPE
};

typedef struct {
	int reason;
	int ax_error;
	int windows;
	int readable;
	int matches;
} cu_ax_diagnostic;

typedef struct { atomic_int cancelled; } cu_cancel_flag;
static cu_cancel_flag *cu_cancel_new(void) { return calloc(1, sizeof(cu_cancel_flag)); }
static void cu_cancel_set(cu_cancel_flag *flag) { atomic_store(&flag->cancelled, 1); }
static int cu_cancelled(cu_cancel_flag *flag) { return flag && atomic_load(&flag->cancelled); }

static int cu_ax_list_result(AXError error, int has_value, int is_array) {
	if (error == kAXErrorAPIDisabled) return CU_AX_DISABLED;
	if (error == kAXErrorNoValue) return CU_AX_NO_VALUE;
	if (error != kAXErrorSuccess) return CU_AX_READ;
	if (!has_value) return CU_AX_NULL_VALUE;
	if (!is_array) return CU_AX_UNEXPECTED_TYPE;
	return CU_AX_OK;
}

static int cu_ax_match_reason(CFIndex windows, int readable, int matches) {
	if (windows > 128) return CU_AX_LIMIT;
	if (matches > 1) return CU_AX_AMBIGUOUS;
	if (windows == 0) return CU_AX_EMPTY;
	if (readable < windows) return CU_AX_GEOMETRY;
	if (matches == 1) return CU_AX_OK;
	return CU_AX_NO_MATCH;
}

static int cu_same_bounds(CGRect a, CGRect b) {
	return fabs(a.origin.x-b.origin.x) < 2 && fabs(a.origin.y-b.origin.y) < 2 &&
		fabs(a.size.width-b.size.width) < 2 && fabs(a.size.height-b.size.height) < 2;
}

static int cu_ax_geometry(AXUIElementRef window, CGRect *bounds) {
	CFTypeRef position = NULL, size = NULL;
	AXUIElementSetMessagingTimeout(window, 0.5);
	int ok = AXUIElementCopyAttributeValue(window, kAXPositionAttribute, &position) == kAXErrorSuccess &&
		AXUIElementCopyAttributeValue(window, kAXSizeAttribute, &size) == kAXErrorSuccess &&
		position && size && CFGetTypeID(position) == AXValueGetTypeID() && CFGetTypeID(size) == AXValueGetTypeID() &&
		AXValueGetValue((AXValueRef)position, kAXValueCGPointType, &bounds->origin) &&
		AXValueGetValue((AXValueRef)size, kAXValueCGSizeType, &bounds->size);
	if (position) CFRelease(position);
	if (size) CFRelease(size);
	return ok;
}

// AXWindows may omit windows on another Space. A main/focused reference alone
// is not sufficient: two CG windows of this process can have identical bounds.
static int cu_cg_unique_bounds_in_list(cu_window selected, CGRect bounds, CFArrayRef list, cu_cancel_flag *flag) {
	if (!list) return 0;
	int total = 0, readable = 0, matches = 0, found = 0;
	CFIndex count = CFArrayGetCount(list);
	if (count > 8192) return 0;
	for (CFIndex i = 0; i < count && !cu_cancelled(flag); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		int32_t pid = 0, layer = 0;
		uint32_t id = 0;
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		if (!p || !CFNumberGetValue(p, kCFNumberSInt32Type, &pid)) { total = 129; break; }
		if (pid != selected.pid) continue;
		if (!l || !CFNumberGetValue(l, kCFNumberSInt32Type, &layer)) { total = 129; break; }
		if (layer != 0) continue;
		total++;
		CGRect current;
		CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		if (!n || !CFNumberGetValue(n, kCFNumberSInt32Type, &id) || !raw || !CGRectMakeWithDictionaryRepresentation(raw, &current)) continue;
		readable++;
		if (cu_same_bounds(current, bounds)) {
			matches++;
			if (id == selected.window_id) found = 1;
		}
	}
	return !cu_cancelled(flag) && found && cu_ax_match_reason(total, readable, matches) == CU_AX_OK;
}

static int cu_cg_unique_bounds(cu_window selected, CGRect bounds, cu_cancel_flag *flag) {
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	int unique = cu_cg_unique_bounds_in_list(selected, bounds, list, flag);
	if (list) CFRelease(list);
	return unique;
}

// Synthetic native inventory for deterministic tests; no desktop reads/input.
static int cu_test_unique_bounds(int scenario) {
	cu_window selected = {.pid = 7, .window_id = 1};
	CGRect bounds = CGRectMake(10, 20, 200, 100);
	CFMutableArrayRef list = CFArrayCreateMutable(NULL, 0, &kCFTypeArrayCallBacks);
	for (int i = 0; i < 2; i++) {
		int32_t pid = (i == 1 && scenario == 2) ? 8 : 7;
		int32_t layer = (i == 1 && scenario == 3) ? 1 : 0;
		int32_t id = (i == 0 && scenario == 5) ? 3 : i+1;
		CGRect rect = bounds;
		if (i == 1 && scenario == 0) rect.origin.x = 300;
		if (i == 0 && scenario == 7) rect.origin.x = 12;
		if (i == 1 && scenario == 8) rect.origin.x = 11.9;
		CFMutableDictionaryRef d = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		CFNumberRef p = CFNumberCreate(NULL, kCFNumberSInt32Type, &pid);
		CFNumberRef l = CFNumberCreate(NULL, kCFNumberSInt32Type, &layer);
		CFNumberRef n = CFNumberCreate(NULL, kCFNumberSInt32Type, &id);
		CFDictionaryRef b = CGRectCreateDictionaryRepresentation(rect);
		CFDictionarySetValue(d, kCGWindowOwnerPID, p);
		CFDictionarySetValue(d, kCGWindowLayer, l);
		CFDictionarySetValue(d, kCGWindowNumber, n);
		if (!(i == 1 && scenario == 4)) CFDictionarySetValue(d, kCGWindowBounds, b);
		CFArrayAppendValue(list, d);
		CFRelease(p); CFRelease(l); CFRelease(n); CFRelease(b); CFRelease(d);
	}
	cu_cancel_flag flag = {0};
	if (scenario == 6) cu_cancel_set(&flag);
	int unique = cu_cg_unique_bounds_in_list(selected, bounds, list, &flag);
	CFRelease(list);
	return unique;
}

static int cu_ax_fallback_allowed(CFIndex windows, int main_valid, int focused_valid, int same_window) {
	return windows == 0 && main_valid && focused_valid && same_window;
}

// Read-only fallback using public AX attributes, never activation or a change
// of Space. Both attributes must agree on one AXWindow owned by the selected
// process, and its geometry must identify exactly the selected CG window.
static AXUIElementRef cu_ax_main_focused_window(AXUIElementRef app, cu_window selected, CGRect bounds, CFIndex list_windows, cu_cancel_flag *flag) {
	if (cu_cancelled(flag)) return NULL;
	CFTypeRef main = NULL, focused = NULL, role = NULL;
	int main_ok = AXUIElementCopyAttributeValue(app, kAXMainWindowAttribute, &main) == kAXErrorSuccess &&
		main && CFGetTypeID(main) == AXUIElementGetTypeID();
	int focused_ok = !cu_cancelled(flag) && AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, &focused) == kAXErrorSuccess &&
		focused && CFGetTypeID(focused) == AXUIElementGetTypeID();
	AXUIElementRef match = NULL;
	if (cu_ax_fallback_allowed(list_windows, main_ok, focused_ok, main_ok && focused_ok && CFEqual(main, focused))) {
		AXUIElementRef window = (AXUIElementRef)main;
		pid_t pid = 0;
		CGRect current;
		AXUIElementSetMessagingTimeout(window, 0.5);
		if (!cu_cancelled(flag) && AXUIElementGetPid(window, &pid) == kAXErrorSuccess && pid == selected.pid &&
			AXUIElementCopyAttributeValue(window, kAXRoleAttribute, &role) == kAXErrorSuccess && role &&
			CFGetTypeID(role) == CFStringGetTypeID() && CFEqual(role, kAXWindowRole) &&
			cu_ax_geometry(window, &current) && cu_same_bounds(current, bounds) &&
			cu_cg_unique_bounds(selected, bounds, flag) &&
			cu_window_exists(selected.window_id, selected.pid, selected.start_sec, selected.start_usec)) {
			match = (AXUIElementRef)CFRetain(window);
		}
	}
	if (role) CFRelease(role);
	if (main) CFRelease(main);
	if (focused) CFRelease(focused);
	return match;
}

// Match a public AX window to the selected CG window by process and unique
// geometry. The optional diagnostic contains only failure stages and counts,
// never element content. Input checks use the same matching path.
static AXUIElementRef cu_ax_window_diagnostic_cancel(cu_window selected, int require_focus, cu_ax_diagnostic *diag, cu_cancel_flag *flag) {
	cu_ax_diagnostic result = {0};
	if (!diag) diag = &result;
	*diag = result;
	if (cu_cancelled(flag)) return NULL;
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
	int list_result = cu_ax_list_result(ax_error, raw != NULL, raw && CFGetTypeID(raw) == CFArrayGetTypeID());
	if (list_result != CU_AX_OK) {
		if (raw) CFRelease(raw);
		CFRelease(app);
		diag->reason = list_result;
		diag->ax_error = (int)ax_error;
		return NULL;
	}
	CFArrayRef windows = (CFArrayRef)raw;
	AXUIElementRef match = NULL;
	CFIndex total = CFArrayGetCount(windows);
	diag->windows = total > INT32_MAX ? INT32_MAX : (int)total;
	int matches = 0, readable = 0;
	for (CFIndex i = 0; i < total && i < 128 && !cu_cancelled(flag); i++) {
		AXUIElementRef window = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
		CGRect current;
		int ok = window && CFGetTypeID(window) == AXUIElementGetTypeID() && cu_ax_geometry(window, &current);
		if (ok) readable++;
		if (ok && cu_same_bounds(current, bounds)) {
			match = window;
			matches++;
		}
	}
	if (cu_cancelled(flag)) { CFRelease(windows); CFRelease(app); return NULL; }
	diag->readable = readable;
	diag->matches = matches;
	diag->reason = cu_ax_match_reason(total, readable, matches);
	if (diag->reason != CU_AX_OK) match = NULL;
	// Only a successfully read empty array permits this fallback. Never hide
	// permission errors, unreadable windows, mismatches, or ambiguous matches.
	AXUIElementRef fallback = NULL;
	if (total == 0 && !cu_cancelled(flag)) {
		fallback = cu_ax_main_focused_window(app, selected, bounds, total, flag);
		if (fallback) { match = fallback; diag->reason = CU_AX_OK; }
	}
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
	if (fallback) CFRelease(fallback);
	CFRelease(windows);
	CFRelease(app);
	return match;
}

static AXUIElementRef cu_ax_window(cu_window selected, int require_focus, cu_cancel_flag *flag) {
	return cu_ax_window_diagnostic_cancel(selected, require_focus, NULL, flag);
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

static void cu_ax_collect(AXUIElementRef element, cu_ax_node *nodes, int cap, int *count, int depth, int ancestor_secure, int *truncated, cu_cancel_flag *flag) {
	if (!element || *count >= cap || depth > 16 || cu_cancelled(flag)) { *truncated = 1; return; }
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
			if (*count >= cap || cu_cancelled(flag)) { *truncated = 1; break; }
			CFTypeRef child = CFArrayGetValueAtIndex(array, i);
			if (child && CFGetTypeID(child) == AXUIElementGetTypeID()) cu_ax_collect((AXUIElementRef)child, nodes, cap, count, depth+1, secure, truncated, flag);
		}
	}
	if (children) CFRelease(children);
}

static int cu_ax_collect_window(cu_window selected, AXUIElementRef expected, cu_ax_node *nodes, int cap, int *truncated, cu_ax_diagnostic *diag, cu_cancel_flag *flag) {
	AXUIElementRef window = cu_ax_window_diagnostic_cancel(selected, 0, diag, flag);
	if (!window) return -1;
	if (!expected || !CFEqual(window, expected)) { CFRelease(window); diag->reason = CU_AX_WINDOW; return -1; }
	int count = 0;
	cu_ax_collect(window, nodes, cap, &count, 0, 0, truncated, flag);
	CFRelease(window);
	return count;
}

static void cu_ax_release(AXUIElementRef element) { if (element) CFRelease(element); }
static int cu_ax_equal(AXUIElementRef a, AXUIElementRef b) { return a && b && CFEqual(a, b); }

// Check identity independently of focus. A different AX window or process is
// target loss, not permission to activate a convenient replacement.
static int cu_selected_focus(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag) {
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	if (!window || !expected || !CFEqual(window, expected)) { if (window) CFRelease(window); return -1; }
	CFRelease(window);
	cu_ax_diagnostic diagnostic;
	window = cu_ax_window_diagnostic_cancel(selected, 1, &diagnostic, flag);
	if (!window) return diagnostic.reason == CU_AX_FOCUS ? 0 : -1;
	int focused = CFEqual(window, expected);
	CFRelease(window);
	return focused ? 1 : -1;
}

// Only a control operation may raise its own pinned window. Snapshot/capture
// and the GUI preview do not call this, so reading cannot steal user focus.
static int cu_focus_selected(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag) {
	if (!expected || cu_cancelled(flag)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	CFRelease(window);
	if (cu_cancelled(flag) || !cu_activation_safe_here(selected) || !cu_activate_running_app(selected.pid, selected.bundle) || cu_cancelled(flag)) return 0;
	window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	if (app) AXUIElementSetMessagingTimeout(app, 0.5);
	if (!cu_cancelled(flag) && app) AXUIElementSetAttributeValue(app, kAXFocusedWindowAttribute, window);
	if (!cu_cancelled(flag)) AXUIElementPerformAction(window, kAXRaiseAction);
	if (app) CFRelease(app);
	CFRelease(window);
	return !cu_cancelled(flag); // actual focus is checked separately before input
}

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
	AXUIElementRef window = cu_ax_window(selected, 1, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int ok = cu_point_ax_safe(window, point, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5 || !cu_point_unobscured(selected, point)) return 0;
	window = cu_ax_window(selected, 1, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	ok = cu_point_ax_safe(window, point, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5 || !cu_point_unobscured(selected, point)) return 0;
	window = cu_ax_window(selected, 1, flag);
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
	AXUIElementRef window = cu_ax_window(selected, 1, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	int ok = cu_focused_control_safe(selected, window, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag)) return 0;
	window = cu_ax_window(selected, 1, flag);
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
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
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
	window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	budget = 128;
	found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) return 0;
	// A failed AX call may still have reached the application. Distinguish it
	// from the checks above, which provably sent no action.
	return AXUIElementPerformAction(element, kAXPressAction) == kAXErrorSuccess ? 1 : -1;
}

static int cu_ax_close_window(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag) {
	if (!expected || cu_cancelled(flag)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
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
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
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
	window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); CFRelease(value); return 0; }
	budget = 128;
	found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) { CFRelease(value); return 0; }
	AXError result = AXUIElementSetAttributeValue(element, kAXValueAttribute, value);
	CFRelease(value);
	return result == kAXErrorSuccess ? 1 : -1;
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
	"os"
	"strings"
	"sync"
	"time"
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
	return c.targets(ctx, false, "")
}

// ApplicationWindows uses a catalog-issued application identity, not a
// model-supplied path, and does not activate the app or switch macOS Spaces.
func (c *MacWindowCatalog) ApplicationWindows(ctx context.Context, app Application) ([]Target, error) {
	c.mu.Lock()
	candidate, ok := c.applications[app.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok || candidate.application != app || candidate.file == nil || app.Identity == "" {
		return nil, errors.New("application candidate changed or expired")
	}
	file, err := os.Stat(candidate.path)
	if err != nil || !os.SameFile(candidate.file, file) {
		return nil, errors.New("application bundle changed before window discovery")
	}
	bundle, _, _, valid := macApplicationMetadata(candidate.path)
	if !valid || bundle != candidate.bundleID {
		return nil, errors.New("application bundle identity changed before window discovery")
	}
	return c.targets(ctx, true, app.Identity)
}

func (c *MacWindowCatalog) targets(ctx context.Context, allSpaces bool, application string) ([]Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	windows, err := collectWindowInventory(ctx, func(buf []C.cu_window) int {
		all := C.int(0)
		if allSpaces {
			all = 1
		}
		return int(C.cu_windows(&buf[0], C.int(len(buf)), all))
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(windows))
	next := make(map[string]C.cu_window, len(windows))
	for _, window := range windows {
		if application != "" && C.GoString(&window.bundle[0]) != application {
			continue
		}
		// The all-Spaces inventory also contains internal menu/decorative
		// records that cannot be resolved as standalone CG windows. They must
		// not create a false multi-window choice for the requested application.
		if allSpaces && C.cu_window_exists(window.window_id, window.pid, window.start_sec, window.start_usec) == 0 {
			continue
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(nonce[:])
		next[id] = window
		detail := fmt.Sprintf("PID %d · Window %d", int32(window.pid), uint32(window.window_id))
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

func macWindowOnScreenFlag(value int) bool {
	return C.cu_test_on_screen(C.int(value)) != 0
}

func macWindowDiagnostic(d C.cu_ax_diagnostic) *WindowDiagnostic {
	reasons := map[int]WindowFailure{
		C.CU_AX_PERMISSION: WindowPermissionDenied, C.CU_AX_WINDOW: WindowUnavailable,
		C.CU_AX_BOUNDS: WindowBoundsMissing, C.CU_AX_APP: WindowAXAppUnavailable,
		C.CU_AX_READ: WindowAXReadFailed, C.CU_AX_EMPTY: WindowAXNoWindows,
		C.CU_AX_NO_VALUE: WindowAXNoValue, C.CU_AX_NULL_VALUE: WindowAXNullValue,
		C.CU_AX_UNEXPECTED_TYPE: WindowAXUnexpectedType,
		C.CU_AX_GEOMETRY:        WindowAXGeometry, C.CU_AX_NO_MATCH: WindowAXNoMatch,
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

func macAXListReason(errorCode, hasValue, isArray int) WindowFailure {
	reason := C.cu_ax_list_result(C.AXError(errorCode), C.int(hasValue), C.int(isArray))
	return macWindowDiagnostic(C.cu_ax_diagnostic{reason: reason}).Reason
}

func macAXFallbackAllowed(windows, mainValid, focusedValid, sameWindow int) bool {
	return C.cu_ax_fallback_allowed(C.CFIndex(windows), C.int(mainValid), C.int(focusedValid), C.int(sameWindow)) != 0
}

func macCGUniqueBoundsFixture(scenario int) bool {
	return C.cu_test_unique_bounds(C.int(scenario)) != 0
}

// Physical input independently proves foreground focus and geometry immediately
// before injection; this identity check alone does not grant that right.
func (c *MacWindowCatalog) Validate(ctx context.Context, target Target, _ bool) error {
	return waitForWindowValidation(ctx, windowSettleTimeout, windowSettleInterval, func() error {
		return c.validateOnce(ctx, target)
	})
}

func (c *MacWindowCatalog) validateOnce(ctx context.Context, target Target) error {
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
	flag := C.cu_cancel_new()
	if flag == nil {
		return errors.New("allocate AX validation cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	var diagnostic C.cu_ax_diagnostic
	current := C.cu_ax_window_diagnostic_cancel(window, 0, &diagnostic, flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	if err := ctx.Err(); err != nil {
		C.cu_ax_release(current)
		return err
	}
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
	flag := C.cu_cancel_new()
	if flag == nil {
		return "", nil, false, errors.New("allocate AX snapshot cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	var truncated C.int
	var diagnostic C.cu_ax_diagnostic
	count := int(C.cu_ax_collect_window(window, c.pinned[target.ID], buffer, cap, &truncated, &diagnostic, flag))
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	if count < 0 {
		if err := ctx.Err(); err != nil {
			return "", nil, false, err
		}
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
		return noInputValidationError(err)
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	element, found := c.refs[ref]
	if c.closed || !ok || !found || c.snapshotID != snapshotID || c.snapshotFor != target.ID {
		c.mu.Unlock()
		return ErrStaleObservation
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
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
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if performed == 0 {
		return semanticInputRefused(ctx, func() error { return c.Validate(ctx, target, false) })
	}
	if performed < 0 {
		return fmt.Errorf("%w: the accessibility action returned an error after dispatch; its effect is unknown and must not be replayed", ErrTargetLost)
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
	if sent != 1 {
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
	flag := C.cu_cancel_new()
	if flag == nil {
		return nil, "", errors.New("allocate capture cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	captured := C.cu_capture_window(C.uint32_t(window.pid), window.window_id, &data, &length, &x, &y, &w, &h, unsafe.Pointer(flag))
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	if data != nil {
		defer C.free(data)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if captured == 0 {
		return nil, "", errors.New("ScreenCaptureKit capture unavailable or not authorized")
	}
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
		return noInputValidationError(err)
	}
	var current macFrameBinding
	if json.Unmarshal([]byte(freshToken), &current) != nil || current != binding || sha256.Sum256(fresh) != binding.Digest {
		return ErrStaleObservation
	}
	changed, err := prepareInputFocus(ctx, 2*time.Second, 100*time.Millisecond,
		func() (bool, error) { return c.selectedFocus(ctx, target) },
		func() error { return c.focusSelected(ctx, target) })
	if err != nil {
		return noInputValidationError(err)
	}
	if changed {
		// Activation can move a window or change its controls. The old frame is
		// never silently rebound to that new UI, even though the target is ours.
		fresh, freshToken, err = c.CaptureBound(ctx, target)
		if err != nil {
			return noInputValidationError(err)
		}
		if json.Unmarshal([]byte(freshToken), &current) != nil || current != binding || sha256.Sum256(fresh) != binding.Digest {
			return ErrStaleObservation
		}
	}
	if err := c.Validate(ctx, target, true); err != nil {
		return noInputValidationError(err)
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		c.mu.Unlock()
		return ErrTargetLost
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return errors.New("allocate input cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	sent := dispatch(window, c.pinned[target.ID], binding, flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if sent == 0 {
		// All three native frame dispatchers return zero only before posting an
		// event. Recheck the same pinned window; never replay the input.
		return frameInputRefused(ctx, func() error { return c.Validate(ctx, target, false) })
	}
	return nil
}

func (c *MacWindowCatalog) selectedFocus(ctx context.Context, target Target) (bool, error) {
	if err := c.Validate(ctx, target, true); err != nil {
		return false, err
	}
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return false, err
	}
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		c.mu.Unlock()
		return false, ErrTargetLost
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return false, errors.New("allocate focus check cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	state := C.cu_selected_focus(window, c.pinned[target.ID], flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if state < 0 {
		return false, frameInputRefused(ctx, func() error { return c.Validate(ctx, target, false) })
	}
	return state == 1, nil
}

func (c *MacWindowCatalog) focusSelected(ctx context.Context, target Target) error {
	if err := c.Validate(ctx, target, true); err != nil {
		return err
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		c.mu.Unlock()
		return ErrTargetLost
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	if C.cu_activation_safe_here(window) == 0 {
		c.mu.Unlock()
		return fmt.Errorf("%w: no input was sent; activating the selected window could change desktops, or its visibility could not be verified; use a semantic action or show the same window", ErrStaleObservation)
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return errors.New("allocate focus cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	raised := C.cu_focus_selected(window, c.pinned[target.ID], flag)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if raised == 0 {
		return frameInputRefused(ctx, func() error { return c.Validate(ctx, target, false) })
	}
	return nil
}
