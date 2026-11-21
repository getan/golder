//go:build darwin && cgo

package computeruse

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework Foundation -framework AppKit -framework ScreenCaptureKit -framework ImageIO -framework CoreMedia -framework CoreImage -framework CoreVideo
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <sys/proc_info.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <dlfcn.h>
#include <time.h>
#include <unistd.h>
#include <math.h>
#include <stdatomic.h>
#include <objc/runtime.h>
#include <objc/message.h>

int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length, double *x, double *y, double *w, double *h, void *cancel);
int cu_stream_latest(int32_t pid, uint32_t windowID, double timeout, void **out, size_t *length, double *at, int *status, double *x, double *y, double *w, double *h, void *cancel);
void cu_stream_stop_all(void);
int cu_capture_window_skylight(uint32_t windowID, void **out, size_t *length, double *x, double *y, double *w, double *h);
int cu_activate_pid(int32_t pid);
int cu_frontmost_pid(void);
int cu_host_app_pid(void);
uint64_t cu_active_space(void);
int cu_restore_space(uint64_t space);
int cu_window_is_on_screen(uint32_t windowID, int32_t pid);
int cu_app_allowed(int32_t pid);
int cu_running_bundle(int32_t pid, char *out, size_t length);
int cu_activate_running_app(int32_t pid, const char *expectedPath);
typedef int (*cu_layout_check)(const void *,AXUIElementRef,int);
void cu_layout_attach(uintptr_t host, uintptr_t key, AXUIElementRef pinned, const void *identity, size_t length, cu_layout_check check);

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

// Current-desktop visibility for preview freshness. A false result means a
// captured frame may be the last composited image, not live content.
int cu_window_is_on_screen(uint32_t windowID, int32_t pid) {
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, windowID);
	if (!list) return 0;
	int on = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t id = 0;
		int32_t owner = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		if (n && p && CFNumberGetValue(n, kCFNumberSInt32Type, &id) && CFNumberGetValue(p, kCFNumberSInt32Type, &owner) &&
			id == windowID && owner == pid) {
			on = cu_window_on_screen(CFDictionaryGetValue(d, kCGWindowIsOnscreen));
			break;
		}
	}
	CFRelease(list);
	return on;
}

enum {
	CU_FOCUS_SAFE = 0, CU_FOCUS_NOT_VISIBLE, CU_FOCUS_INVENTORY,
	CU_FOCUS_PEER_METADATA, CU_FOCUS_OFFSCREEN_PEER, CU_FOCUS_MULTIPLE_WINDOWS
};

// Only real, independently resolvable windows count as peers. CG's all-Spaces
// list also contains menu/decorative records that cannot receive app focus.
static int cu_activation_peer_relevant(uint32_t selected, uint32_t peer, int layer, int resolvable) {
	return selected != peer && layer == 0 && resolvable;
}

// App activation can make macOS jump to the app's last-used Space instead of
// the intended window. Preserve that boundary, but report the exact refusal.
static int cu_activation_reason(cu_window selected, int operator) {
	if (!operator && !cu_window_visible_here(selected)) return CU_FOCUS_NOT_VISIBLE;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionAll | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) return CU_FOCUS_INVENTORY;
	int reason = CU_FOCUS_SAFE;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t id = 0;
		int32_t pid = 0, layer = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef p = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowLayer);
		if (!p || !CFNumberGetValue(p, kCFNumberSInt32Type, &pid) || pid != selected.pid) continue;
		if (!n || !l || !CFNumberGetValue(n, kCFNumberSInt32Type, &id) || !CFNumberGetValue(l, kCFNumberSInt32Type, &layer)) { reason = CU_FOCUS_PEER_METADATA; break; }
		if (id == selected.window_id || layer != 0) continue;
		int resolvable = cu_window_exists(id, selected.pid, selected.start_sec, selected.start_usec);
		if (!cu_activation_peer_relevant(selected.window_id, id, layer, resolvable)) continue;
		// A person's deliberate click may bring the sole selected window from
		// another Space. Never choose among multiple real application windows.
		if (operator) { reason = CU_FOCUS_MULTIPLE_WINDOWS; break; }
		CGRect bounds;
		CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(d, kCGWindowBounds);
		if (!raw || !CGRectMakeWithDictionaryRepresentation(raw, &bounds)) { reason = CU_FOCUS_PEER_METADATA; break; }
		if (bounds.size.width >= 1 && bounds.size.height >= 1 && !cu_window_on_screen(CFDictionaryGetValue(d, kCGWindowIsOnscreen))) { reason = CU_FOCUS_OFFSCREEN_PEER; break; }
	}
	CFRelease(list);
	return reason;
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

enum { CU_INPUT_UNVERIFIED, CU_INPUT_OCCLUDED, CU_INPUT_AX_HIT, CU_INPUT_GEOMETRY };
// Point verification outcomes, ordered from fully verified to unverifiable.
enum { CU_POINT_OK = 0, CU_POINT_TARGET_NOT_LISTED, CU_POINT_FOREIGN_HIT, CU_POINT_UNVERIFIABLE };
// Only the synchronous native dispatcher writes refusal; cancellation touches
// only the atomic flag, and Go reads refusal after the dispatcher returns.
typedef struct {
	atomic_int cancelled;
	int refusal;
	int point;
	AXUIElementRef reviewed;
	int coordinate;
	double px, py;
	char stage[48];
} cu_cancel_flag;
static cu_cancel_flag *cu_cancel_new(void) { return calloc(1, sizeof(cu_cancel_flag)); }
static void cu_cancel_set(cu_cancel_flag *flag) { atomic_store(&flag->cancelled, 1); }
static int cu_cancelled(cu_cancel_flag *flag) { return flag && atomic_load(&flag->cancelled); }
// First failed native check wins; it never contains window or user content.
static void cu_stage(cu_cancel_flag *flag, const char *stage) {
	if (flag && stage && !flag->stage[0]) snprintf(flag->stage, sizeof(flag->stage), "%s", stage);
}

// SkyLight's per-pid post takes the IOHID path that Chromium accepts; the
// public CGEventPostToPid is ignored as dead input by Chromium/Electron.
typedef void (*cu_sl_post_fn)(pid_t, CGEventRef);
static void *cu_skylight(void) {
	static void *handle;
	static int tried;
	if (!tried) {
		tried = 1;
		handle = dlopen("/System/Library/PrivateFrameworks/SkyLight.framework/SkyLight", RTLD_LAZY);
	}
	return handle;
}
static cu_sl_post_fn cu_sl_post(void) {
	static cu_sl_post_fn fn;
	void *h = cu_skylight();
	if (!fn && h) fn = (cu_sl_post_fn)dlsym(h, "SLEventPostToPid");
	return fn;
}
typedef void (*cu_set_window_location_fn)(CGEventRef, double, double);
typedef void (*cu_set_int_field_fn)(CGEventRef, uint32_t, int64_t);
static cu_set_window_location_fn cu_set_window_location(void) {
	static cu_set_window_location_fn fn;
	void *h = cu_skylight();
	if (!fn && h) fn = (cu_set_window_location_fn)dlsym(h, "CGEventSetWindowLocation");
	return fn;
}
static cu_set_int_field_fn cu_set_int_field(void) {
	static cu_set_int_field_fn fn;
	void *h = cu_skylight();
	if (!fn && h) fn = (cu_set_int_field_fn)dlsym(h, "SLEventSetIntegerValueField");
	return fn;
}
typedef void (*cu_set_auth_fn)(CGEventRef, id);
static cu_set_auth_fn cu_set_auth(void) {
	static cu_set_auth_fn fn;
	void *h = cu_skylight();
	if (!fn && h) fn = (cu_set_auth_fn)dlsym(h, "SLEventSetAuthenticationMessage");
	return fn;
}

// The SLSEventRecord pointer is embedded in the CGEvent at a version-dependent
// offset; probe the same slots as Cua's Swift/Rust bridge.
static void *cu_event_record(CGEventRef event) {
	unsigned char *base = (unsigned char *)event;
	size_t offsets[] = {24, 32, 16};
	for (size_t i = 0; i < sizeof(offsets)/sizeof(offsets[0]); i++) {
		void *record = NULL;
		memcpy(&record, base + offsets[i], sizeof(record));
		if (record) return record;
	}
	return NULL;
}

// Chromium and Electron on macOS 14+ drop synthetic keyboard events that do
// not carry SLSEventAuthenticationMessage. Build it with the private factory
// and attach it before the post; older systems without the selector fall back
// to a plain SkyLight post.
static void cu_attach_auth(CGEventRef event, pid_t pid) {
	static Class cls;
	static SEL sel;
	static int tried;
	if (!tried) {
		tried = 1;
		cls = objc_getClass("SLSEventAuthenticationMessage");
		sel = sel_registerName("messageWithEventRecord:pid:version:");
	}
	// class_respondsToSelector reports false for this class on macOS 27 even
	// though the factory exists; class_getClassMethod is authoritative.
	if (!cls || !class_getClassMethod(cls, sel)) return;
	void *record = cu_event_record(event);
	if (!record) return;
	id message = ((id (*)(id, SEL, void *, int32_t, uint32_t))objc_msgSend)((id)cls, sel, record, (int32_t)pid, 0);
	if (!message) return;
	cu_set_auth_fn set = cu_set_auth();
	if (set) set(event, message);
}

static void cu_post_event(pid_t pid, CGEventRef event) {
	cu_sl_post_fn fn = cu_sl_post();
	if (fn) fn(pid, event);
	else CGEventPostToPid(pid, event);
}
static void cu_post_key(pid_t pid, CGEventRef event) {
	cu_attach_auth(event, pid);
	cu_post_event(pid, event);
}
static void cu_post_mouse(pid_t pid, CGEventRef event) {
	cu_post_event(pid, event);
}

// Diagnostics for native tests: bit 1 class, 2 selector, 4 setter, 8 record,
// 16 authentication message attached.
int cu_auth_probe(void) {
	int bits = 0;
	Class cls = objc_getClass("SLSEventAuthenticationMessage");
	SEL sel = sel_registerName("messageWithEventRecord:pid:version:");
	if (cls) bits |= 1;
	if (cls && class_getClassMethod(cls, sel)) bits |= 2;
	if (cu_set_auth()) bits |= 4;
	CGEventRef event = CGEventCreateKeyboardEvent(NULL, 0, true);
	if (event) {
		if (cu_event_record(event)) bits |= 8;
		cu_attach_auth(event, getpid());
		typedef id (*cu_get_auth_fn)(CGEventRef);
		cu_get_auth_fn get = (cu_get_auth_fn)dlsym(cu_skylight(), "SLEventGetAuthenticationMessage");
		if (get && get(event)) bits |= 16;
		CFRelease(event);
	}
	return bits;
}

int cu_auth_methods(char *out, size_t length) {
	Class cls = objc_getClass("SLSEventAuthenticationMessage");
	if (!cls || !out || length == 0) return 0;
	Class meta = object_getClass((id)cls);
	unsigned int count = 0;
	Method *methods = class_copyMethodList(meta, &count);
	if (!methods) return 0;
	size_t used = 0;
	for (unsigned int i = 0; i < count; i++) {
		const char *name = sel_getName(method_getName(methods[i]));
		int n = snprintf(out + used, length - used, "%s\n", name);
		if (n > 0 && (size_t)n < length - used) used += (size_t)n;
	}
	free(methods);
	return (int)used;
}

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
	char block[48];
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

// An absent optional subrole is normal on Chromium text fields. An AX
// transport error is not evidence of absence. Never inspect text values.
static const char *cu_ax_block_reason(CFTypeRef role, CFTypeRef subrole, AXError rc) {
	if (!role || CFGetTypeID(role) != CFStringGetTypeID() || !CFStringGetLength(role)) return "role_unreadable";
	if (CFEqual(role, CFSTR("AXSecureTextField"))) return "secure_field";
	if (subrole && CFEqual(subrole, CFSTR("AXSecureTextField"))) return "secure_field";
	if (!cu_ax_text_role(role)) return NULL;
	if (!subrole && (rc == kAXErrorNoValue || rc == kAXErrorAttributeUnsupported)) return NULL;
	if (rc != kAXErrorSuccess || !subrole || CFGetTypeID(subrole) != CFStringGetTypeID() || !CFStringGetLength(subrole)) return "text_subrole_unreadable";
	return NULL;
}

static const char *cu_ax_read_block(AXUIElementRef element, CFTypeRef role) {
	CFTypeRef subrole = NULL;
	AXError rc = AXUIElementCopyAttributeValue(element, kAXSubroleAttribute, &subrole);
	const char *block = cu_ax_block_reason(role, subrole, rc);
	if (subrole) CFRelease(subrole);
	return block;
}

static void cu_ax_collect(AXUIElementRef element, cu_ax_node *nodes, int cap, int *count, int depth, int ancestor_secure, int *truncated, cu_cancel_flag *flag) {
	if (!element || *count >= cap || depth > 16 || cu_cancelled(flag)) { *truncated = 1; return; }
	AXUIElementSetMessagingTimeout(element, 0.3);
	CFTypeRef role = NULL, title = NULL, enabled = NULL, value = NULL;
	AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role);
	const char *reason = cu_ax_read_block(element, role);
	int secure = ancestor_secure || reason != NULL;
	cu_ax_node *node = &nodes[(*count)++];
	node->element = (AXUIElementRef)CFRetain(element);
	node->secure = secure;
	if (reason) snprintf(node->block, sizeof(node->block), "control_%s", reason);
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

// Chromium and Electron can expose a richer accessibility tree after the
// application is asked through AXManualAccessibility. This is only a hint:
// failures are ignored, and every identity, focus and occlusion check remains
// mandatory before any semantic or coordinate action.
static AXError cu_ax_request_manual_accessibility(pid_t pid, int electron, cu_cancel_flag *flag) {
	if (cu_cancelled(flag)) return kAXErrorCannotComplete;
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	if (!app) return kAXErrorCannotComplete;
	AXUIElementSetMessagingTimeout(app, 0.3);
	AXError result = AXUIElementSetAttributeValue(app, CFSTR("AXManualAccessibility"), kCFBooleanTrue);
	if (electron && result == kAXErrorAttributeUnsupported && !cu_cancelled(flag)) {
		result = AXUIElementSetAttributeValue(app, CFSTR("AXEnhancedUserInterface"), kCFBooleanTrue);
	}
	CFRelease(app);
	return result;
}

static int cu_ax_collect_window(cu_window selected, AXUIElementRef expected, cu_ax_node *nodes, int cap, int *truncated, int *hint, int electron, cu_ax_diagnostic *diag, cu_cancel_flag *flag) {
	AXUIElementRef window = cu_ax_window_diagnostic_cancel(selected, 0, diag, flag);
	if (!window) return -1;
	if (!expected || !CFEqual(window, expected)) { CFRelease(window); diag->reason = CU_AX_WINDOW; return -1; }
	if (hint) *hint = cu_ax_request_manual_accessibility(selected.pid, electron, flag);
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

static int cu_raise_pinned(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag) {
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	if (!window || !expected || !CFEqual(window, expected) || cu_cancelled(flag)) { if (window) CFRelease(window); return 0; }
	// AXRaise does not restore a minimized window; clear that state first.
	CFTypeRef minimized = NULL;
	if (AXUIElementCopyAttributeValue(window, kAXMinimizedAttribute, &minimized) == kAXErrorSuccess && minimized &&
		CFGetTypeID(minimized) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)minimized)) {
		AXUIElementSetAttributeValue(window, kAXMinimizedAttribute, kCFBooleanFalse);
	}
	if (minimized) CFRelease(minimized);
	int raised = AXUIElementPerformAction(window, kAXRaiseAction) == kAXErrorSuccess;
	if (window) CFRelease(window);
	return raised;
}

// Only a control operation may raise its own pinned window. Snapshot/capture
// and the GUI preview do not call this, so reading cannot steal user focus.
static int cu_focus_selected(cu_window selected, AXUIElementRef expected, cu_cancel_flag *flag, int operator) {
	if (!expected || cu_cancelled(flag)) return 0;
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	CFRelease(window);
	if (cu_cancelled(flag) || cu_activation_reason(selected, operator) != CU_FOCUS_SAFE || !cu_activate_running_app(selected.pid, selected.bundle) || cu_cancelled(flag)) return 0;
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

// Called on the exact retained AX window, never on the frontmost app. Mode 2
// is exclusively a person's explicit one-time presentation from fullscreen.
static int cu_layout_identity(const void *raw, AXUIElementRef expected, int mode) {
	cu_window w = *(const cu_window *)raw;
	if (!cu_window_exists(w.window_id,w.pid,w.start_sec,w.start_usec)) return 0;
	cu_cancel_flag flag = {0};
	AXUIElementRef current = cu_ax_window(w,0,&flag);
	int same = current && expected && CFEqual(current,expected);
	if (current) CFRelease(current);
	if (!same) return 0;
	if (mode == 2) return cu_focus_selected(w,expected,&flag,1);
	return cu_window_visible_here(w);
}

static void cu_begin_layout(uintptr_t host, uintptr_t key, AXUIElementRef pinned, cu_window w) {
	cu_layout_attach(host,key,pinned,&w,sizeof(w),cu_layout_identity);
}

static int cu_ax_contains(AXUIElementRef element, AXUIElementRef wanted, int depth, int *budget, cu_cancel_flag *flag) {
	if (!element || depth > 16 || *budget <= 0 || cu_cancelled(flag)) return 0;
	(*budget)--;
	AXUIElementSetMessagingTimeout(element, 0.3);
	CFTypeRef role = NULL;
	AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role);
	int secure = cu_ax_read_block(element, role) != NULL;
	if (role) CFRelease(role);
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

// Read-only hit test: AXUIElementCopyElementAtPosition resolves which window
// would actually receive the event at this point. It never injects an event.
// A same-pid hit is not enough: the top-level window must be the pinned one,
// otherwise another window of the same application would receive the input.
static int cu_point_hits_target(cu_window selected, AXUIElementRef expected, CGPoint point, cu_cancel_flag *flag) {
	AXUIElementRef system = AXUIElementCreateSystemWide();
	AXUIElementRef hit = NULL;
	AXError rc = system ? AXUIElementCopyElementAtPosition(system, point.x, point.y, &hit) : kAXErrorCannotComplete;
	int result = CU_POINT_UNVERIFIABLE;
	if (rc == kAXErrorSuccess && hit) {
		pid_t pid = 0;
		if (AXUIElementGetPid(hit, &pid) != kAXErrorSuccess) result = CU_POINT_UNVERIFIABLE;
		else if (pid != selected.pid) result = CU_POINT_FOREIGN_HIT;
		else if (!expected) result = CU_POINT_OK;
		else {
			CFTypeRef top = NULL;
			if (AXUIElementCopyAttributeValue(hit, kAXTopLevelUIElementAttribute, &top) == kAXErrorSuccess && top &&
				CFGetTypeID(top) == AXUIElementGetTypeID()) {
				result = CFEqual((AXUIElementRef)top, expected) ? CU_POINT_OK : CU_POINT_FOREIGN_HIT;
			} else {
				result = CU_POINT_UNVERIFIABLE;
			}
			if (top) CFRelease(top);
		}
	}
	if (hit) CFRelease(hit);
	if (system) CFRelease(system);
	if (cu_cancelled(flag)) return CU_POINT_UNVERIFIABLE;
	return result;
}

// Pure classification so the decision table is testable without real windows.
static int cu_point_classify(int found, int metadata, int overlap, int hit) {
	if (metadata) return CU_POINT_UNVERIFIABLE;
	if (!found) return CU_POINT_TARGET_NOT_LISTED;
	if (!overlap) return CU_POINT_OK;
	return hit;
}

// Rect overlap with a higher window is a lead, not proof of occlusion: a
// window-level alpha and bounds say nothing about per-pixel transparency, and
// input-transparent overlays (screen overlays, security agents) are common.
// Classify into: target not visible, overlapping windows, foreign hit, or an
// unresolvable point. Only a proven same-pid hit verifies input reaches it.
static int cu_point_unobscured(cu_window selected, AXUIElementRef expected, CGPoint point, cu_cancel_flag *flag) {
	flag->point = CU_POINT_OK;
	CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!list) { flag->point = CU_POINT_UNVERIFIABLE; return 0; }
	int found = 0, overlap = 0, metadata = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(list); i++) {
		CFDictionaryRef item = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
		uint32_t number = 0;
		int32_t layer = 0;
		CFNumberRef n = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowNumber);
		CFNumberRef l = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowLayer);
		if (!n || !l || !CFNumberGetValue(n, kCFNumberSInt32Type, &number) || !CFNumberGetValue(l, kCFNumberSInt32Type, &layer)) { metadata = 1; break; }
		if (number == selected.window_id) { found = 1; break; }
		if (layer < 0) continue;
		CGRect bounds;
		CFDictionaryRef raw = (CFDictionaryRef)CFDictionaryGetValue(item, kCGWindowBounds);
		if (!raw || !CGRectMakeWithDictionaryRepresentation(raw, &bounds)) { metadata = 1; break; }
		double alpha = 1;
		CFNumberRef a = (CFNumberRef)CFDictionaryGetValue(item, kCGWindowAlpha);
		if ((a && !CFNumberGetValue(a, kCFNumberDoubleType, &alpha)) || !isfinite(alpha) || alpha < 0 || alpha > 1) { metadata = 1; break; }
		if (alpha > 0 && CGRectContainsPoint(bounds, point)) overlap = 1;
	}
	CFRelease(list);
	// Overlap alone proves nothing; resolve it with a read-only hit test.
	int hit = overlap ? cu_point_hits_target(selected, expected, point, flag) : CU_POINT_OK;
	int verdict = cu_point_classify(found, metadata, overlap, hit);
	flag->point = verdict;
	if (verdict != CU_POINT_OK) { flag->refusal = CU_INPUT_OCCLUDED; return 0; }
	return 1;
}

// Stable labels for Go. Order mirrors the CU_POINT_* enum.
static const char *cu_point_verdict_name(int verdict) {
	switch (verdict) {
		case CU_POINT_TARGET_NOT_LISTED: return "target_not_visible";
		case CU_POINT_FOREIGN_HIT: return "foreign_hit";
		case CU_POINT_UNVERIFIABLE: return "unverified";
		default: return "verified";
	}
}

// Exposes the pure decision table to Go tests without real windows.
int cu_point_classify_test(int found, int metadata, int overlap, int hit, const char **out) {
	if (!out) return -1;
	*out = cu_point_verdict_name(cu_point_classify(found, metadata, overlap, hit));
	return 0;
}

int cu_ax_block_reason_test(const char *role, const char *subrole, int response, const char **out) {
	if (!out) return -1;
	CFStringRef r = role ? CFStringCreateWithCString(NULL, role, kCFStringEncodingUTF8) : NULL;
	CFStringRef s = subrole ? CFStringCreateWithCString(NULL, subrole, kCFStringEncodingUTF8) : NULL;
	AXError rc = response == 0 ? kAXErrorSuccess : response == 1 ? kAXErrorNoValue : response == 2 ? kAXErrorAttributeUnsupported : kAXErrorCannotComplete;
	const char *reason = cu_ax_block_reason(r, s, rc);
	*out = reason ? reason : "";
	if (r) CFRelease(r);
	if (s) CFRelease(s);
	return 0;
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
	int ok = 0;
	int budget = 128;
	if (flag->reviewed) {
		// The reviewed control can be hidden behind an AXGroup image map or a
		// Chromium container, so verify the control itself at the point rather
		// than requiring the system hit to be its exact topmost element.
		CGRect reviewedBounds;
		ok = cu_ax_geometry(flag->reviewed, &reviewedBounds) && CGRectContainsPoint(reviewedBounds, point) &&
			cu_direct_control(flag->reviewed);
		if (ok) ok = cu_ax_contains(window, flag->reviewed, 0, &budget, flag);
	} else {
		AXUIElementRef system = AXUIElementCreateSystemWide();
		AXUIElementRef hit = NULL;
		ok = system && AXUIElementCopyElementAtPosition(system, point.x, point.y, &hit) == kAXErrorSuccess && hit &&
			!CFEqual(window, hit) && cu_direct_control(hit);
		if (ok) ok = cu_ax_contains(window, hit, 0, &budget, flag);
		if (hit) CFRelease(hit);
		if (system) CFRelease(system);
	}
	if (!ok) flag->refusal = CU_INPUT_AX_HIT;
	return ok;
}

static int cu_frame_bounds_match(cu_window selected, double x, double y, double w, double h, cu_cancel_flag *flag) {
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds)) return 0;
	if (fabs(bounds.origin.x-x) <= 0.5 && fabs(bounds.origin.y-y) <= 0.5 && fabs(bounds.size.width-w) <= 0.5 && fabs(bounds.size.height-h) <= 0.5) return 1;
	flag->refusal = CU_INPUT_GEOMETRY;
	return 0;
}

typedef struct { int member, safe, editable, visible, tiles, verdict; char role[32], block[48]; AXUIElementRef control; } cu_probe;

// 4x4 tiles, row-major. A reviewed control can span several tiles; region
// comparison must cover every one of them rather than trusting a model mask.
static int cu_ax_tile_mask(CGRect window, CGRect control) {
	if (CGRectIsEmpty(window) || CGRectIsEmpty(control)) return 0;
	int mask = 0;
	for (int y = 0; y < 4; y++) {
		for (int x = 0; x < 4; x++) {
			CGRect tile = CGRectMake(window.origin.x + window.size.width*x/4,
				window.origin.y + window.size.height*y/4,
				window.size.width/4 + 1, window.size.height/4 + 1);
			if (CGRectIntersectsRect(tile, control)) mask |= 1 << (y*4+x);
		}
	}
	return mask;
}

// Bounded fallback when AXUIElementCopyElementAtPosition returns a container.
// It never crosses a secure/unknown subtree and returns only text controls or
// buttons; a container is never promoted to an input target.
static AXUIElementRef cu_ax_find_control_at_point(AXUIElementRef element, CGPoint point, int depth, int *budget, cu_cancel_flag *flag) {
	if (!element || depth > 16 || *budget <= 0 || cu_cancelled(flag)) return NULL;
	(*budget)--;
	AXUIElementSetMessagingTimeout(element, 0.3);
	CFTypeRef role = NULL;
	AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role);
	int secure = cu_ax_read_block(element, role) != NULL;
	int text = cu_ax_text_role(role);
	int button = role && CFGetTypeID(role) == CFStringGetTypeID() && CFEqual(role, CFSTR("AXButton"));
	if (role) CFRelease(role);
	if (secure) return NULL;
	if (text || button) {
		CGRect bounds;
		if (cu_ax_geometry(element, &bounds) && CGRectContainsPoint(bounds, point)) return (AXUIElementRef)CFRetain(element);
		return NULL;
	}
	CGRect bounds;
	if (cu_ax_geometry(element, &bounds) && !CGRectContainsPoint(bounds, point)) return NULL;
	CFTypeRef children = NULL;
	AXUIElementRef found = NULL;
	if (AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, &children) == kAXErrorSuccess && children && CFGetTypeID(children) == CFArrayGetTypeID()) {
		CFArrayRef array = (CFArrayRef)children;
		for (CFIndex i = 0; i < CFArrayGetCount(array) && !found; i++) {
			CFTypeRef child = CFArrayGetValueAtIndex(array, i);
			if (child && CFGetTypeID(child) == AXUIElementGetTypeID()) found = cu_ax_find_control_at_point((AXUIElementRef)child, point, depth+1, budget, flag);
		}
	}
	if (children) CFRelease(children);
	return found;
}

// Independent of the bounded children walk: a hit can be absent from AXChildren
// yet have a parent chain to the pinned window. Never return arbitrary strings
// from AX, inspect a value, synthesize an event or activate the application.
static int cu_probe_point(cu_window selected, AXUIElementRef expected, CGRect bounds, CGPoint p, cu_cancel_flag *flag, cu_probe *out) {
	CGRect current;
	if (!expected || !cu_current_bounds(selected, &current) || !CGRectEqualToRect(current, bounds)) return 0;
	out->visible = cu_point_unobscured(selected, expected, p, flag);
	out->verdict = flag->point;
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid), hit = NULL;
	if (!app) return 1;
	AXUIElementSetMessagingTimeout(app, 0.3);
	AXError rc = AXUIElementCopyElementAtPosition(app,p.x,p.y,&hit);
	CFRelease(app);
	if (rc != kAXErrorSuccess || !hit) {
		// A Chromium window can return no element at all from the system hit
		// test while still exposing a text control in its AX children. Search
		// the pinned window root before declaring that there is no AX hit.
		if (hit) CFRelease(hit);
		int budget = 128;
		hit = cu_ax_find_control_at_point(expected, p, 0, &budget, flag);
		if (!hit) return 1;
	}
	if (!cu_direct_control(hit)) {
		// Chromium can return a container while exposing a text control deeper
		// in the tree. Search only this pinned window, bounded and non-secure.
		int budget = 128;
		AXUIElementRef found = cu_ax_find_control_at_point(expected, p, 0, &budget, flag);
		if (found) {
			CFRelease(hit);
			hit = found;
		}
	}
	pid_t pid = 0;
	AXUIElementRef node = (AXUIElementRef)CFRetain(hit);
	int safe = 1, text = 0;
	const char *label = "other";
	for (int i=0; node && i<32 && !cu_cancelled(flag); i++) {
		if (AXUIElementGetPid(node,&pid) != kAXErrorSuccess || pid != selected.pid) break;
		AXUIElementSetMessagingTimeout(node,0.3);
		CFTypeRef role=NULL;
		AXUIElementCopyAttributeValue(node,kAXRoleAttribute,&role);
		const char *reason = cu_ax_read_block(node, role);
		if (safe && reason) {
			safe = 0;
			snprintf(out->block, sizeof(out->block), "%s_%s", i ? "ancestor" : "control", reason);
		}
		if (i==0 && role && CFGetTypeID(role)==CFStringGetTypeID()) {
			text=cu_ax_text_role(role);
			if (CFEqual(role,CFSTR("AXTextField"))) label="AXTextField";
			else if (CFEqual(role,CFSTR("AXTextArea"))) label="AXTextArea";
			else if (CFEqual(role,CFSTR("AXButton"))) label="AXButton";
			else if (CFEqual(role,CFSTR("AXGroup"))) label="AXGroup";
			else if (CFEqual(role,CFSTR("AXWebArea"))) label="AXWebArea";
		}
		if (role) CFRelease(role);
		if (CFEqual(node,expected)) { out->member=1; break; }
		CFTypeRef parent=NULL;
		if (AXUIElementCopyAttributeValue(node,kAXParentAttribute,&parent)!=kAXErrorSuccess || !parent || CFGetTypeID(parent)!=AXUIElementGetTypeID()) { if (parent) CFRelease(parent); break; }
		CFRelease(node); node=(AXUIElementRef)parent;
	}
	if (node) CFRelease(node);
	if (hit) {
		// Role names are classification metadata, not user content. Record the
		// hit even when it is outside the authorized window or sensitive so the
		// caller can distinguish no-element, wrong-window and sensitive hits.
		strcpy(out->role,label);
		out->safe = safe;
		int control = out->member && safe && !cu_cancelled(flag) && (text || strcmp(label,"AXButton")==0);
		if (control) {
			out->control = (AXUIElementRef)CFRetain(hit);
			CGRect control;
			if (cu_ax_geometry(hit, &control)) out->tiles = cu_ax_tile_mask(bounds, control);
		}
		Boolean settable=false;
		out->editable=control && text && AXUIElementIsAttributeSettable(hit,kAXValueAttribute,&settable)==kAXErrorSuccess && settable;
	}
	CFRelease(hit);
	return cu_current_bounds(selected,&current) && CGRectEqualToRect(current,bounds);
}

static int cu_frame_point_safe(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, cu_cancel_flag *flag, CGPoint *out, int operator) {
	if (!expected || cu_cancelled(flag) || !isfinite(bx) || !isfinite(by) || !isfinite(bw) || !isfinite(bh) || !isfinite(nx) || !isfinite(ny) || bw <= 0 || bh <= 0 || nx < 0 || nx >= 1 || ny < 0 || ny >= 1) return 0;
	CGPoint point = CGPointMake(bx + nx*bw, by + ny*bh);
	// Two AX hit checks, each followed by fresh geometry, stacking and focus
	// checks. The last pass verifies the pinned window just before posting.
	for (int pass = 0; pass < 3; pass++) {
		if (cu_cancelled(flag) || !cu_frame_bounds_match(selected, bx, by, bw, bh, flag) || !cu_point_unobscured(selected, expected, point, flag)) return 0;
		AXUIElementRef window = cu_ax_window(selected, 1, flag);
		if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
		int ok = operator || flag->coordinate || cu_point_ax_safe(window, point, flag);
		CFRelease(window);
		if (!ok) return 0;
	}
	if (cu_cancelled(flag)) return 0;
	*out = point;
	return 1;
}

static int cu_click_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, cu_cancel_flag *flag, int operator) {
	CGPoint point;
	if (!cu_frame_point_safe(selected, expected, bx, by, bw, bh, nx, ny, flag, &point, operator)) return 0;
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
	if (!cu_frame_point_safe(selected, expected, bx, by, bw, bh, nx, ny, flag, &point, 0)) return 0;
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
	if (ok && flag->reviewed) ok = CFEqual(focused,flag->reviewed);
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
		ok = cu_point_unobscured(selected, NULL, point, flag);
	}
	if (position) CFRelease(position);
	if (size) CFRelease(size);
	if (focused) CFRelease(focused);
	if (app) CFRelease(app);
	return ok;
}

// Keyboard delivery is addressed to the application's focused control. It does
// not require the app to be frontmost or the point unobscured; the key is
// posted to the target pid, never to whatever window happens to be in front.
static int cu_key_target_safe(cu_window selected, AXUIElementRef window, cu_cancel_flag *flag) {
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	CFTypeRef focused = NULL;
	int ok = app && AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &focused) == kAXErrorSuccess && focused &&
		CFGetTypeID(focused) == AXUIElementGetTypeID() && !CFEqual(window, focused) && cu_direct_control((AXUIElementRef)focused);
	if (ok && flag->reviewed) ok = CFEqual(focused, flag->reviewed);
	int budget = 128;
	if (ok) ok = cu_ax_contains(window, (AXUIElementRef)focused, 0, &budget, flag);
	if (focused) CFRelease(focused);
	if (app) CFRelease(app);
	return ok;
}

// Chromium may replace the input node when it gains focus, so identity is not
// stable across the click. Accept a focused text control only at the same
// on-screen frame.
static int cu_focused_text_at(AXUIElementRef app, CGRect want, cu_cancel_flag *flag) {
	if (!app) return 0;
	CFTypeRef current = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &current) != kAXErrorSuccess || !current ||
		CFGetTypeID(current) != AXUIElementGetTypeID()) {
		if (current) CFRelease(current);
		return 0;
	}
	CGRect got;
	CFTypeRef role = NULL;
	int ok = cu_ax_geometry((AXUIElementRef)current, &got) &&
		fabs(got.origin.x-want.origin.x) <= 1 && fabs(got.origin.y-want.origin.y) <= 1 &&
		fabs(got.size.width-want.size.width) <= 1 && fabs(got.size.height-want.size.height) <= 1 &&
		AXUIElementCopyAttributeValue((AXUIElementRef)current, kAXRoleAttribute, &role) == kAXErrorSuccess &&
		cu_ax_text_role(role) && !cu_ax_read_block((AXUIElementRef)current, role);
	if (role) CFRelease(role);
	CFRelease(current);
	return ok;
}

typedef int (*cu_post_record_fn)(const void *, const unsigned char *);
typedef int (*cu_get_owner_fn)(uint32_t, uint32_t, uint32_t *);
typedef int (*cu_conn_psn_fn)(uint32_t, void *);
typedef uint32_t (*cu_conn_id_fn)(void);
typedef int (*cu_get_pid_psn_fn)(pid_t, void *);

static void cu_put_le32(unsigned char *p, uint32_t v) {
	p[0] = (unsigned char)(v & 0xff);
	p[1] = (unsigned char)((v >> 8) & 0xff);
	p[2] = (unsigned char)((v >> 16) & 0xff);
	p[3] = (unsigned char)((v >> 24) & 0xff);
}

static int cu_psn_for_window(uint32_t wid, pid_t pid, unsigned char psn[8]) {
	void *h = cu_skylight();
	if (h) {
		cu_conn_id_fn conn = (cu_conn_id_fn)dlsym(h, "CGSMainConnectionID");
		cu_get_owner_fn owner = (cu_get_owner_fn)dlsym(h, "SLSGetWindowOwner");
		cu_conn_psn_fn psnfn = (cu_conn_psn_fn)dlsym(h, "SLSGetConnectionPSN");
		if (conn && owner && psnfn) {
			uint32_t cid = conn(), owner_cid = 0;
			if (owner(cid, wid, &owner_cid) == 0 && owner_cid != 0 && psnfn(owner_cid, psn) == 0) return 1;
		}
	}
	cu_get_pid_psn_fn fallback = (cu_get_pid_psn_fn)dlsym(RTLD_DEFAULT, "GetProcessForPID");
	return fallback && fallback(pid, psn) == 0;
}

// Private make-key/focus records. Public AXFocusedWindow alone does not make
// the NSWindow key, so a background click is ignored for focus.
typedef int (*cu_ax_get_window_fn)(AXUIElementRef, CGWindowID *);
static CGWindowID cu_focused_window_id(pid_t pid) {
	cu_ax_get_window_fn get = (cu_ax_get_window_fn)dlsym(RTLD_DEFAULT, "_AXUIElementGetWindow");
	if (!get) return 0;
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	if (!app) return 0;
	CFTypeRef focused = NULL;
	CGWindowID wid = 0;
	if (AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, &focused) == kAXErrorSuccess && focused) {
		get((AXUIElementRef)focused, &wid);
		CFRelease(focused);
	}
	CFRelease(app);
	return wid;
}

static int cu_make_key_without_raise(cu_window selected, cu_cancel_flag *flag) {
	void *h = cu_skylight();
	if (!h) return 0;
	cu_post_record_fn post = (cu_post_record_fn)dlsym(h, "SLPSPostEventRecordTo");
	if (!post) return 0;
	unsigned char psn[8] = {0};
	if (!cu_psn_for_window(selected.window_id, selected.pid, psn)) return 0;
	unsigned char record[0xF8] = {0};
	// Defocus the app's current key window first; without it a focus record
	// alone is often dropped while the app is still handling the old key.
	CGWindowID current = cu_focused_window_id(selected.pid);
	if (current && current != selected.window_id) {
		record[0x04] = 0xF8;
		record[0x08] = 0x0D;
		cu_put_le32(record + 0x3C, current);
		record[0x8A] = 0x02;
		if (post(psn, record) != 0) return 0;
		usleep(120000);
		memset(record, 0, sizeof(record));
	}
	// Focus the exact window: record kind 0x0d, direction 1 at 0x8a.
	record[0x04] = 0xF8;
	record[0x08] = 0x0D;
	cu_put_le32(record + 0x3C, selected.window_id);
	record[0x8A] = 0x01;
	if (post(psn, record) != 0) return 0;
	usleep(120000);
	for (int i = 0; i < 2; i++) {
		memset(record, 0, sizeof(record));
		record[0x04] = 0xF8;
		record[0x08] = i == 0 ? 0x01 : 0x02; // make-key press/release
		record[0x3A] = 0x10;
		cu_put_le32(record + 0x3C, selected.window_id);
		memset(record + 0x20, 0xFF, 0x10);
		if (cu_cancelled(flag) || post(psn, record) != 0) return 0;
	}
	usleep(120000);
	return 1;
}

// Cua's Chromium/macOS 27 recipe: move, an off-screen down/up to wake the
// renderer's input route, then the real down/up at the target. Every event
// carries the gesture phase, click group and window-routing fields.
static int cu_chromium_click(cu_window selected, CGPoint screen, CGPoint window_local, cu_cancel_flag *flag) {
	cu_set_window_location_fn setloc = cu_set_window_location();
	cu_set_int_field_fn setint = cu_set_int_field();
	if (!setloc || !setint) { cu_stage(flag, "focus_spi"); return 0; }
	CGEventSourceRef source = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	if (!source) { cu_stage(flag, "focus_source"); return 0; }
	int64_t group = (int64_t)((time(NULL) & 0xffff) << 16) | (selected.pid & 0xffff);
	CGPoint off = CGPointMake(-1, -1);
	struct {
		CGEventType type;
		CGPoint location;
		CGPoint window_location;
		int phase, click_state;
		useconds_t delay;
	} steps[] = {
		{kCGEventMouseMoved, screen, window_local, 2, 0, 15000},
		{kCGEventLeftMouseDown, off, off, 1, 1, 1000},
		{kCGEventLeftMouseUp, off, off, 2, 1, 100000},
		{kCGEventLeftMouseDown, screen, window_local, 3, 1, 1000},
		{kCGEventLeftMouseUp, screen, window_local, 3, 1, 0},
	};
	int posted = 1;
	for (size_t i = 0; i < sizeof(steps)/sizeof(steps[0]); i++) {
		if (cu_cancelled(flag)) { posted = 0; break; }
		CGEventRef event = CGEventCreateMouseEvent(source, steps[i].type, steps[i].location, kCGMouseButtonLeft);
		if (!event) { cu_stage(flag, "focus_event"); posted = 0; break; }
		setloc(event, steps[i].window_location.x, steps[i].window_location.y);
		setint(event, 0, steps[i].phase);       // kCGMouseEventNumber (gesture phase)
		setint(event, 1, steps[i].click_state); // kCGMouseEventClickState
		setint(event, 3, 0);                    // left button
		setint(event, 7, 3);                    // NSEventSubtypeTouch
		setint(event, 40, selected.pid);        // Chromium synthetic-event filter
		setint(event, 51, selected.window_id);  // windowNumber
		setint(event, 91, selected.window_id);  // window under pointer
		setint(event, 92, selected.window_id);  // window that can handle the event
		setint(event, 58, group);               // one gesture for the whole sequence
		cu_post_mouse(selected.pid, event);
		CFRelease(event);
		if (steps[i].delay) usleep(steps[i].delay);
	}
	CFRelease(source);
	if (!posted) return 0;
	return 1;
}

// Focus a reviewed field without raising its app: one click is posted to the
// target pid, then the app's focused element is required to become that exact
// node. Web inputs often refuse AXFocused writes, so this is the only way to
// put the caret in a background search box.
static int cu_focus_field(cu_window selected, AXUIElementRef expected, AXUIElementRef element, cu_cancel_flag *flag) {
	CGRect frame;
	if (!cu_ax_geometry(element, &frame) || frame.size.width < 2 || frame.size.height < 2) { cu_stage(flag, "focus_frame"); return 0; }
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	int budget = 128;
	int member = window && expected && CFEqual(window, expected) && cu_ax_contains(window, element, 0, &budget, flag);
	if (member && window) {
		// Make the exact window AppKit-key without activating the application.
		// Background clicks are otherwise ignored for focus by Chromium.
		AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
		if (app) {
			AXUIElementSetMessagingTimeout(app, 0.5);
			AXUIElementSetAttributeValue(app, kAXFocusedWindowAttribute, window);
			CFRelease(app);
		}
	}
	if (window) CFRelease(window);
	if (!member || cu_cancelled(flag)) { cu_stage(flag, "focus_member"); return 0; }
	cu_make_key_without_raise(selected, flag); // best effort; focus is rechecked below
	usleep(50000);
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds)) { cu_stage(flag, "focus_bounds"); return 0; }
	CGPoint center = CGPointMake(frame.origin.x + frame.size.width/2, frame.origin.y + frame.size.height/2);
	CGPoint local = CGPointMake(center.x - bounds.origin.x, center.y - bounds.origin.y);
	if (!cu_chromium_click(selected, center, local, flag)) return 0;
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	int focused = 0;
	if (app) AXUIElementSetMessagingTimeout(app, 0.5);
	for (int i = 0; app && i < 12 && !focused && !cu_cancelled(flag); i++) {
		focused = cu_focused_text_at(app, frame, flag);
		if (!focused) usleep(25000);
	}
	if (app) CFRelease(app);
	if (!focused) cu_stage(flag, "focus_click");
	return focused;
}

static int cu_press_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, uint16_t code, cu_cancel_flag *flag) {
	if (!expected || cu_cancelled(flag) || !isfinite(bx) || !isfinite(by) || !isfinite(bw) || !isfinite(bh) || bw <= 0 || bh <= 0) { cu_stage(flag, "press_args"); return 0; }
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5) { cu_stage(flag, "press_bounds"); return 0; }
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { cu_stage(flag, "press_window"); if (window) CFRelease(window); return 0; }
	int ok = cu_key_target_safe(selected, window, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag)) { cu_stage(flag, "press_focus"); return 0; }
	window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { cu_stage(flag, "press_window2"); if (window) CFRelease(window); return 0; }
	ok = cu_key_target_safe(selected, window, flag);
	CFRelease(window);
	if (!ok || cu_cancelled(flag) || !cu_current_bounds(selected, &bounds) || fabs(bounds.origin.x-bx) > 0.5 || fabs(bounds.origin.y-by) > 0.5 || fabs(bounds.size.width-bw) > 0.5 || fabs(bounds.size.height-bh) > 0.5) { cu_stage(flag, "press_focus2"); return 0; }
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, code, true);
	CGEventRef up = CGEventCreateKeyboardEvent(NULL, code, false);
	if (!down || !up || cu_cancelled(flag)) { cu_stage(flag, "press_event"); if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
	CGEventSetFlags(down, 0);
	CGEventSetFlags(up, 0);
	cu_post_key(selected.pid, down);
	cu_post_key(selected.pid, up);
	CFRelease(down);
	CFRelease(up);
	return 1;
}

static AXUIElementRef cu_text_frame_control(cu_window selected, AXUIElementRef expected, CGRect bounds, cu_cancel_flag *flag, int operator) {
	if (!expected || cu_cancelled(flag)) return NULL;
	CGRect current;
	if (!cu_current_bounds(selected, &current) || !CGRectEqualToRect(current, bounds)) return NULL;
	AXUIElementRef window = cu_ax_window(selected, 1, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return NULL; }
	if (flag->coordinate) {
		// A reviewed graphical click is not proof of the keyboard destination.
		// Continue through the focused-field check before agent text input.
		CGPoint point = CGPointMake(bounds.origin.x + flag->px*bounds.size.width, bounds.origin.y + flag->py*bounds.size.height);
		if (!cu_point_unobscured(selected, expected, point, flag)) { CFRelease(window); return NULL; }
	}
	// A person can type into their chosen opaque canvas.
	if (operator) {
		if (!cu_window_visible_here(selected)) { CFRelease(window); return NULL; }
		return window;
	}
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	if (app) AXUIElementSetMessagingTimeout(app, 0.5);
	CFTypeRef focused = NULL, role = NULL, enabled = NULL;
	int ok = app && AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &focused) == kAXErrorSuccess &&
		focused && CFGetTypeID(focused) == AXUIElementGetTypeID();
	AXUIElementRef element = (AXUIElementRef)focused;
	pid_t pid = 0;
	int budget = 128;
	if (ok) {
		AXUIElementSetMessagingTimeout(element, 0.5);
		ok = AXUIElementGetPid(element, &pid) == kAXErrorSuccess && pid == selected.pid &&
			AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role) == kAXErrorSuccess && cu_ax_text_role(role) &&
			!cu_ax_read_block(element, role) &&
			AXUIElementCopyAttributeValue(element, kAXEnabledAttribute, &enabled) == kAXErrorSuccess &&
			enabled && CFGetTypeID(enabled) == CFBooleanGetTypeID() && CFBooleanGetValue((CFBooleanRef)enabled) &&
			cu_ax_contains(window, element, 0, &budget, flag) && cu_focused_control_safe(selected, window, flag);
	}
	if (ok) CFRetain(element);
	if (focused) CFRelease(focused);
	if (role) CFRelease(role);
	if (enabled) CFRelease(enabled);
	if (app) CFRelease(app);
	CFRelease(window);
	return ok ? element : NULL;
}

// Returns zero only before posting any keyboard input, and -1 if a multi-chunk
// insertion stopped after dispatch. Every chunk rechecks the same focused AX
// control and pinned window; no clipboard, shortcuts, or implicit submission.
static int cu_type_text_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, const char *text, size_t length, cu_cancel_flag *flag, int operator) {
	if (!text || length == 0 || length > 4096 || !isfinite(bx) || !isfinite(by) || !isfinite(bw) || !isfinite(bh) || bw <= 0 || bh <= 0) return 0;
	CFStringRef value = CFStringCreateWithBytes(NULL, (const UInt8 *)text, (CFIndex)length, kCFStringEncodingUTF8, false);
	if (!value) return 0;
	CGRect bounds = CGRectMake(bx, by, bw, bh);
	AXUIElementRef initial = cu_text_frame_control(selected, expected, bounds, flag, operator);
	if (!initial) { CFRelease(value); return 0; }
	UniChar buffer[4096];
	CFIndex total = CFStringGetLength(value);
	if (total > 4096) { CFRelease(initial); CFRelease(value); return 0; }
	CFStringGetCharacters(value, CFRangeMake(0, total), buffer);
	CFRelease(value);
	int sent = 0;
	for (CFIndex offset = 0; offset < total;) {
		AXUIElementRef current = cu_text_frame_control(selected, expected, bounds, flag, operator);
		int safe = current && CFEqual(current, initial);
		if (current) CFRelease(current);
		if (!safe || cu_cancelled(flag)) { CFRelease(initial); return sent ? -1 : 0; }
		CFIndex count = total-offset < 20 ? total-offset : 20;
		// Never split a UTF-16 surrogate pair across keyboard events.
		if (offset+count < total && CFStringIsSurrogateHighCharacter(buffer[offset+count-1])) count--;
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
		CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
		if (!down || !up || cu_cancelled(flag)) {
			if (down) CFRelease(down);
			if (up) CFRelease(up);
			CFRelease(initial);
			return sent ? -1 : 0;
		}
		CGEventSetFlags(down, 0);
		CGEventSetFlags(up, 0);
		CGEventKeyboardSetUnicodeString(down, count, buffer+offset);
		CGEventKeyboardSetUnicodeString(up, count, buffer+offset);
		CGEventPostToPid(selected.pid, down);
		CGEventPostToPid(selected.pid, up);
		CFRelease(down);
		CFRelease(up);
		sent = 1;
		offset += count;
	}
	CFRelease(initial);
	return sent;
}

// Raw Unicode typing to one pid. Web contenteditable controls often have no AX
// text element, so the semantic type path cannot address them; the click puts
// the caret in place and these key events deliver the text to that pid.
static int cu_type_unicode_raw(pid_t pid, const char *text, size_t length, cu_cancel_flag *flag) {
	if (!text || length == 0 || length > 4096) return 0;
	CFStringRef value = CFStringCreateWithBytes(NULL, (const UInt8 *)text, (CFIndex)length, kCFStringEncodingUTF8, false);
	if (!value) return 0;
	UniChar buffer[4096];
	CFIndex total = CFStringGetLength(value);
	if (total > 4096) { CFRelease(value); return 0; }
	CFStringGetCharacters(value, CFRangeMake(0, total), buffer);
	CFRelease(value);
	for (CFIndex offset = 0; offset < total;) {
		if (cu_cancelled(flag)) return -1;
		CFIndex count = total-offset < 20 ? total-offset : 20;
		if (offset+count < total && CFStringIsSurrogateHighCharacter(buffer[offset+count-1])) count--;
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
		CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
		if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return -1; }
		CGEventSetFlags(down, 0);
		CGEventSetFlags(up, 0);
		CGEventKeyboardSetUnicodeString(down, count, buffer+offset);
		CGEventKeyboardSetUnicodeString(up, count, buffer+offset);
		cu_post_key(pid, down);
		cu_post_key(pid, up);
		CFRelease(down);
		CFRelease(up);
		offset += count;
	}
	return 1;
}

// One capture-bound pixel action: click the reviewed point, then type into the
// focused field without requiring an AX text element.
int cu_type_at_frame(cu_window selected, AXUIElementRef expected, double bx, double by, double bw, double bh, double nx, double ny, const char *text, size_t length, cu_cancel_flag *flag, int operator, int submit) {
	if (cu_cancelled(flag)) return 0;
	// Electron web content may not be visible at dispatch time. Use the
	// window-local Chromium click path instead of the visible-point check;
	// the frame lease already bound the target and geometry.
	CGPoint screen = CGPointMake(bx + nx*bw, by + ny*bh);
	CGPoint local = CGPointMake(nx*bw, ny*bh);
	if (!cu_chromium_click(selected, screen, local, flag)) return 0;
	usleep(60000);
	int typed = 1;
	if (length > 0) typed = cu_type_unicode_raw(selected.pid, text, length, flag);
	if (typed != 1 || !submit) return typed;
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, 36, true);
	CGEventRef up = CGEventCreateKeyboardEvent(NULL, 36, false);
	if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return -1; }
	cu_post_key(selected.pid, down);
	cu_post_key(selected.pid, up);
	CFRelease(down);
	CFRelease(up);
	return 1;
}

static int cu_ax_invoke(cu_window selected, AXUIElementRef expected, AXUIElementRef element, CFStringRef action, cu_cancel_flag *flag) {
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
	int listed = AXUIElementCopyActionNames(element, &actions) == kAXErrorSuccess && actions;
	allowed = listed && CFArrayContainsValue(actions, CFRangeMake(0, CFArrayGetCount(actions)), action);
	if (actions) CFRelease(actions);
	if (cu_cancelled(flag)) return 0;
	if (!listed) return -4; // no AX action sent; callers may choose the keyboard path
	if (!allowed) return -2;
	window = cu_ax_window(selected, 0, flag);
	if (!window || !CFEqual(window, expected)) { if (window) CFRelease(window); return 0; }
	budget = 128;
	found = cu_ax_contains(window, element, 0, &budget, flag);
	CFRelease(window);
	if (!found || cu_cancelled(flag)) return 0;
	// A failed AX call may still have reached the application. Distinguish it
	// from the checks above, which provably sent no action.
	return AXUIElementPerformAction(element, action) == kAXErrorSuccess ? 1 : -1;
}

static int cu_ax_press(cu_window selected, AXUIElementRef expected, AXUIElementRef element, cu_cancel_flag *flag) {
	int rc = cu_ax_invoke(selected, expected, element, kAXPressAction, flag);
	return (rc == -2 || rc == -4) ? 0 : rc;
}

static int cu_ax_submit(cu_window selected, AXUIElementRef expected, AXUIElementRef element, cu_cancel_flag *flag, int keyboard) {
	CFTypeRef role = NULL;
	int field = element && AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role) == kAXErrorSuccess &&
		role && CFEqual(role, CFSTR("AXTextField"));
	if (role) CFRelease(role);
	if (!field) { cu_stage(flag, "submit_role"); return 0; }
	CGRect want;
	if (!cu_ax_geometry(element, &want)) { cu_stage(flag, "submit_frame"); return 0; }
	// Chromium may replace the input node when its suggestion popup opens.
	// Reuse the app's focused field only when it occupies the same frame.
	AXUIElementRef resolved = NULL;
	AXUIElementRef target = element;
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	CFTypeRef focused = NULL;
	if (app) {
		AXUIElementSetMessagingTimeout(app, 0.5);
		if (AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, &focused) == kAXErrorSuccess && focused &&
			CFGetTypeID(focused) == AXUIElementGetTypeID() && !CFEqual(focused, element)) {
			CGRect got;
			CFTypeRef frole = NULL;
			if (cu_ax_geometry((AXUIElementRef)focused, &got) &&
				fabs(got.origin.x-want.origin.x) <= 1 && fabs(got.origin.y-want.origin.y) <= 1 &&
				fabs(got.size.width-want.size.width) <= 1 && fabs(got.size.height-want.size.height) <= 1 &&
				AXUIElementCopyAttributeValue((AXUIElementRef)focused, kAXRoleAttribute, &frole) == kAXErrorSuccess &&
				cu_ax_text_role(frole) && !cu_ax_read_block((AXUIElementRef)focused, frole)) {
				resolved = (AXUIElementRef)CFRetain(focused);
				target = resolved;
			}
			if (frole) CFRelease(frole);
		}
		if (focused) CFRelease(focused);
		CFRelease(app);
	}
	int result = 0;
	if (!keyboard) {
		result = cu_ax_invoke(selected, expected, target, kAXConfirmAction, flag);
		if (result == 0) cu_stage(flag, "submit_confirm");
	} else {
		// Key delivery is addressed to the target pid; frontmost is not required.
		AXUIElementRef window = cu_ax_window(selected, 0, flag);
		int member = 0, budget = 128;
		if (!window || !expected || !CFEqual(window, expected)) {
			cu_stage(flag, "submit_window");
		} else {
			member = cu_ax_contains(window, target, 0, &budget, flag);
			if (!member) cu_stage(flag, "submit_member");
		}
		if (window) CFRelease(window);
		if (member && !cu_cancelled(flag)) {
			AXUIElementRef currentApp = AXUIElementCreateApplication(selected.pid);
			int focusedNow = 0;
			if (currentApp) {
				AXUIElementSetMessagingTimeout(currentApp, 0.5);
				focusedNow = cu_focused_text_at(currentApp, want, flag);
				CFRelease(currentApp);
			}
			if (!focusedNow && !cu_cancelled(flag)) focusedNow = cu_focus_field(selected, expected, target, flag);
			if (!focusedNow) {
				cu_stage(flag, "submit_focus");
				result = -3; // no Return posted
			} else {
				CGRect b;
				if (cu_cancelled(flag) || !cu_current_bounds(selected, &b)) {
					cu_stage(flag, "submit_bounds");
					result = 0;
				} else {
					flag->reviewed = target; // cu_press_frame requires this exact focused control twice
					result = cu_press_frame(selected, expected, b.origin.x, b.origin.y, b.size.width, b.size.height, 36, flag);
					// Focus can return to the operator between verification and the
					// final frontmost check. Those stages prove no key was posted,
					// so restore once and retry exactly that pre-send check.
					if (result == 0 && (strcmp(flag->stage, "press_window") == 0 || strcmp(flag->stage, "press_window2") == 0) &&
						!cu_cancelled(flag) && cu_focus_selected(selected, expected, flag, 0)) {
						flag->stage[0] = 0;
						result = cu_press_frame(selected, expected, b.origin.x, b.origin.y, b.size.width, b.size.height, 36, flag);
					}
					if (result == 0) cu_stage(flag, "submit_dispatch");
				}
			}
		}
	}
	if (resolved) CFRelease(resolved);
	return result;
}

// Chromium may stop painting a window that is not visible (including another
// Space) after it accepts input. A one-pixel scroll down and back asks the
// renderer to commit a frame without switching Spaces or navigating.
static void cu_repaint_nudge(cu_window selected) {
	CGRect bounds;
	if (!cu_current_bounds(selected, &bounds)) return;
	CGEventSourceRef source = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	if (!source) return;
	CGPoint point = CGPointMake(bounds.origin.x + bounds.size.width/2, bounds.origin.y + bounds.size.height/2);
	for (int delta = 1; delta >= -1; delta -= 2) {
		CGEventRef event = CGEventCreateScrollWheelEvent(source, kCGScrollEventUnitPixel, 1, delta);
		if (!event) continue;
		CGEventSetLocation(event, point);
		cu_set_int_field_fn setint = cu_set_int_field();
		if (setint) {
			setint(event, 40, selected.pid);
			setint(event, 51, selected.window_id);
			setint(event, 91, selected.window_id);
			setint(event, 92, selected.window_id);
		}
		cu_post_mouse(selected.pid, event);
		CFRelease(event);
		usleep(30000);
	}
	CFRelease(source);
}

// Replaces a web text field through real key events: select all, then type.
// Chromium ignores AXValue writes, but accepts per-pid key delivery.
static int cu_ax_replace(cu_window selected, AXUIElementRef expected, AXUIElementRef element, const char *text, size_t length, cu_cancel_flag *flag) {
	if (!element || !text || length > 4096 || cu_cancelled(flag)) return 0;
	CFTypeRef role = NULL;
	int field = AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &role) == kAXErrorSuccess && cu_ax_text_role(role);
	if (role) CFRelease(role);
	if (!field) { cu_stage(flag, "replace_role"); return 0; }
	AXUIElementRef window = cu_ax_window(selected, 0, flag);
	int budget = 128;
	int member = window && expected && CFEqual(window, expected) && cu_ax_contains(window, element, 0, &budget, flag);
	if (window) CFRelease(window);
	if (!member || cu_cancelled(flag)) { cu_stage(flag, "replace_member"); return 0; }
	AXUIElementRef app = AXUIElementCreateApplication(selected.pid);
	int focused = 0;
	CGRect frame;
	if (!cu_ax_geometry(element, &frame)) { if (app) CFRelease(app); cu_stage(flag, "replace_frame"); return 0; }
	if (app) {
		AXUIElementSetMessagingTimeout(app, 0.5);
		focused = cu_focused_text_at(app, frame, flag);
		CFRelease(app);
	}
	if (!focused && !cu_cancelled(flag)) focused = cu_focus_field(selected, expected, element, flag);
	if (!focused || cu_cancelled(flag)) { cu_stage(flag, "replace_focus"); return -3; }
	// End, then bounded backspaces. Cmd+A is not reliably honored by this
	// web input, while these keys clear any preceding content.
	CGEventRef endDown = CGEventCreateKeyboardEvent(NULL, 119, true);
	CGEventRef endUp = CGEventCreateKeyboardEvent(NULL, 119, false);
	if (endDown && endUp) {
		cu_post_key(selected.pid, endDown);
		cu_post_key(selected.pid, endUp);
	}
	if (endDown) CFRelease(endDown);
	if (endUp) CFRelease(endUp);
	usleep(20000);
	for (int i = 0; i < 64 && !cu_cancelled(flag); i++) {
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 51, true);
		CGEventRef up = CGEventCreateKeyboardEvent(NULL, 51, false);
		if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return -1; }
		cu_post_key(selected.pid, down);
		cu_post_key(selected.pid, up);
		CFRelease(down);
		CFRelease(up);
		if ((i & 7) == 7) usleep(2000);
	}
	usleep(30000);
	CFStringRef value = CFStringCreateWithBytes(NULL, (const UInt8 *)text, (CFIndex)length, kCFStringEncodingUTF8, false);
	if (!value) return -1; // select-all was already sent
	UniChar buffer[4096];
	CFIndex total = CFStringGetLength(value);
	if (total > 4096) { CFRelease(value); return -1; }
	CFStringGetCharacters(value, CFRangeMake(0, total), buffer);
	CFRelease(value);
	int sent = 0;
	for (CFIndex offset = 0; offset < total;) {
		if (cu_cancelled(flag)) return -1;
		CFIndex count = total-offset < 20 ? total-offset : 20;
		if (offset+count < total && CFStringIsSurrogateHighCharacter(buffer[offset+count-1])) count--;
		CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
		CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
		if (!down || !up || cu_cancelled(flag)) { if (down) CFRelease(down); if (up) CFRelease(up); return -1; }
		CGEventSetFlags(down, 0);
		CGEventSetFlags(up, 0);
		CGEventKeyboardSetUnicodeString(down, count, buffer+offset);
		CGEventKeyboardSetUnicodeString(up, count, buffer+offset);
		cu_post_key(selected.pid, down);
		cu_post_key(selected.pid, up);
		CFRelease(down);
		CFRelease(up);
		sent = 1;
		offset += count;
	}
	if (sent) cu_repaint_nudge(selected);
	return sent;
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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/cgo"
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
	snapshotAX   AXTreeStatus
	axInit       axEnablement
	layoutGate   func(context.Context) error
	reviewed     macPointReview // at most one retained control per catalog, never exposed to the model
	closed       bool
	homePID      C.int32_t
	homeSpace    C.uint64_t
}

// Captured once when the Golder process starts: for the TUI this is the
// terminal hosting it, for the GUI the Golder process itself. Never derive
// home from whichever app happens to be frontmost when a session is created.
var (
	initialFrontmostPID = C.cu_frontmost_pid()
	initialSpace        = C.cu_active_space()
)

func NewMacWindowCatalog() *MacWindowCatalog {
	home := C.cu_host_app_pid()
	if home <= 0 {
		home = initialFrontmostPID
	}
	return &MacWindowCatalog{
		windows:   make(map[string]C.cu_window),
		pinned:    make(map[string]C.AXUIElementRef),
		homePID:   home,
		homeSpace: initialSpace,
	}
}

// EnableDesktopLayout is called only by the GUI after attaching the local
// control grant. Installing it does not present, move or activate any window.
func (s *Session) EnableDesktopLayout(host uintptr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.backend.(*MacWindowCatalog)
	if !ok || host == 0 || s.desktop != nil || !s.status.Active || !s.status.Capabilities.Control {
		return
	}
	id := s.status.Target.ID
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.windows[id]
	if !ok || c.pinned[id] == 0 || c.closed {
		return
	}
	d := &desktopChoice{}
	s.desktop = d
	c.layoutGate = func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if !s.status.Active || s.status.Paused || s.status.Target.ID != id {
			return ErrPaused
		}
		if d.expire(s.sequence) {
			return nil
		}
		return d.beforeInput(s.sequence)
	}
	h := cgo.NewHandle(layoutGrant{s: s, choice: d, target: id})
	C.cu_begin_layout(C.uintptr_t(host), C.uintptr_t(h), c.pinned[id], w)
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

func macActivationPeerRelevant(selected, peer uint32, layer, resolvable int) bool {
	return C.cu_activation_peer_relevant(C.uint32_t(selected), C.uint32_t(peer), C.int(layer), C.int(resolvable)) != 0
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
	c.clearPointReviewLocked()
	c.clearSnapshotLocked()
	C.cu_stream_stop_all()
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
	stamp := axProcess{int32(window.pid), uint64(window.start_sec), uint64(window.start_usec)}
	request := c.axInit.needsRequest(stamp, time.Now())
	var hint C.int
	var hintPtr *C.int
	if request {
		hintPtr = &hint
		bundle := C.GoString(&window.bundle[0])
		info, err := os.Stat(filepath.Join(bundle, "Contents", "Frameworks", "Electron Framework.framework"))
		c.axInit = axEnablement{process: stamp, electron: err == nil && info.IsDir()}
	}
	var electron C.int
	if c.axInit.electron {
		electron = 1
	}
	collect := func(h *C.int) int {
		return int(C.cu_ax_collect_window(window, c.pinned[target.ID], buffer, cap, &truncated, h, electron, &diagnostic, flag))
	}
	count := collect(hintPtr)
	if count >= 0 && request && ctx.Err() == nil {
		switch hint {
		case C.kAXErrorSuccess:
			c.axInit.hint = "accepted"
		case C.kAXErrorAttributeUnsupported, C.kAXErrorNotImplemented:
			c.axInit.hint = "unsupported"
		default:
			c.axInit.hint = "not_accepted"
			c.axInit.retry = time.Now().Add(3 * time.Second)
		}
		// Only resample a sparse Electron tree, once per enablement attempt.
		// Drop the first pass's retained handles and cached strings before reuse.
		if electron != 0 && count <= 16 && truncated == 0 && c.axInit.hint != "unsupported" {
			nodes := unsafe.Slice(buffer, count)
			ready := false
			for i := range nodes {
				ready = ready || nodes[i].editable != 0 || C.GoString(&nodes[i].role[0]) == "AXWebArea"
			}
			if !ready && waitForAXRefresh(ctx, 400*time.Millisecond) {
				for i := range nodes {
					C.cu_ax_release(nodes[i].element)
				}
				clear(unsafe.Slice(buffer, cap))
				count = collect(nil)
			}
		}
	}
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
		elements = append(elements, Element{Ref: ref, Role: C.GoString(&nodes[i].role[0]), Name: C.GoString(&nodes[i].name[0]), Value: C.GoString(&nodes[i].value[0]), Enabled: nodes[i].enabled != 0, Editable: nodes[i].editable != 0, Secure: nodes[i].secure != 0, ControlBlock: C.GoString(&nodes[i].block[0])})
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
	c.snapshotAX = AXTreeStatus{ManualAccessibility: c.axInit.hint, WebContent: observedWebContent(c.axInit.electron, elements, truncated != 0)}
	return c.snapshotID, elements, truncated != 0, nil
}

func (c *MacWindowCatalog) snapshotAXStatus(id string) AXTreeStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id == "" || c.snapshotID != id {
		return AXTreeStatus{}
	}
	return c.snapshotAX
}

func (*MacWindowCatalog) SupportsAction(action string) bool {
	return action == "click" || action == "fill" || action == "replace" || action == "submit"
}

// Act executes a targeted AXPress or non-secure AXValue replacement on a node
// from the bound backend snapshot. Target and membership are rechecked before
// the OS request; cancellation cannot undo an already-sent action.
func (c *MacWindowCatalog) Act(ctx context.Context, target Target, snapshotID, ref, action, text string) error {
	if !c.SupportsAction(action) || (action != "fill" && action != "replace" && text != "") || len(text) > 4096 || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return ErrNotAllowed
	}
	if action == "submit" {
		return routeFieldSubmit(func() error { return c.actNative(ctx, target, snapshotID, ref, action, text, false) }, func() error {
			return c.actNative(ctx, target, snapshotID, ref, action, text, true)
		})
	}
	return c.actNative(ctx, target, snapshotID, ref, action, text, false)
}

func (c *MacWindowCatalog) actNative(ctx context.Context, target Target, snapshotID, ref, action, text string, keyboard bool) error {
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
	} else if action == "submit" {
		var mode C.int
		if keyboard {
			mode = 1
		}
		performed = C.cu_ax_submit(window, c.pinned[target.ID], element, flag, mode)
	} else if action == "replace" {
		value := C.CString(text)
		performed = C.cu_ax_replace(window, c.pinned[target.ID], element, value, C.size_t(len(text)), flag)
		C.free(unsafe.Pointer(value))
	} else {
		value := C.CString(text)
		performed = C.cu_ax_fill(window, c.pinned[target.ID], element, value, C.size_t(len(text)), flag)
		C.free(unsafe.Pointer(value))
	}
	if !stop() {
		<-done
	}
	stage := C.GoString(&flag.stage[0])
	C.free(unsafe.Pointer(flag))
	if performed != -2 && performed != -4 && performed != -3 {
		c.clearSnapshotLocked()
	}
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if performed == -2 {
		return errNoConfirmAction
	}
	if performed == -4 {
		return errNoConfirmAction
	}
	if performed == -3 {
		return inputRefused(ctx, func() error { return c.Validate(ctx, target, false) }, staleReason("field_focus_unconfirmed", "the selected field did not acquire keyboard focus; Return was not sent"))
	}
	if performed == 0 {
		return semanticInputRefusedStage(ctx, func() error { return c.Validate(ctx, target, false) }, stage)
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

func (c *MacWindowCatalog) WindowOnScreen(ctx context.Context, target Target) (bool, error) {
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	c.mu.Unlock()
	if !ok {
		return false, ErrTargetLost
	}
	return C.cu_window_is_on_screen(window.window_id, window.pid) != 0, nil
}

type macFrameBinding struct {
	Target string
	X, Y   float64
	W, H   float64
	Nonce  string `json:"n"`
	Tiles  []byte `json:"t,omitempty"`
}

type macPointReview struct {
	nonce      string
	point      [2]float64
	regions    uint16
	control    C.AXUIElementRef
	coordinate bool
}

func (c *MacWindowCatalog) clearPointReviewLocked() {
	if c.reviewed.control != 0 {
		C.cu_ax_release(c.reviewed.control)
	}
	c.reviewed = macPointReview{}
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
	var captured C.int
	if C.cu_window_is_on_screen(window.window_id, window.pid) == 0 {
		// ScreenCaptureKit and one-shot captures return the last composited
		// image off the current Space. Read the backing store directly.
		captured = C.cu_capture_window_skylight(window.window_id, &data, &length, &x, &y, &w, &h)
	}
	if captured == 0 {
		var at C.double
		var frameStatus C.int
		captured = C.cu_stream_latest(C.int32_t(window.pid), window.window_id, 1.5, &data, &length, &at, &frameStatus, &x, &y, &w, &h, unsafe.Pointer(flag))
	}
	if captured == 0 {
		captured = C.cu_capture_window(C.uint32_t(window.pid), window.window_id, &data, &length, &x, &y, &w, &h, unsafe.Pointer(flag))
	}
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
	pixels, err := fingerprintFrame(image)
	if err != nil {
		return nil, "", fmt.Errorf("decode captured frame: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", err
	}
	binding, err := json.Marshal(macFrameBinding{Target: target.ID, X: float64(x), Y: float64(y), W: float64(w), H: float64(h), Nonce: hex.EncodeToString(nonce[:]), Tiles: pixels.tiles})
	if err != nil {
		return nil, "", err
	}
	return image, string(binding), nil
}

func (c *MacWindowCatalog) InspectPoint(ctx context.Context, target Target, token string, x, y float64) (PointInspection, error) {
	var old, now macFrameBinding
	if json.Unmarshal([]byte(token), &old) != nil || old.Target != target.ID || !(x >= 0 && x < 1 && y >= 0 && y < 1) {
		return PointInspection{}, ErrStaleObservation
	}
	_, fresh, err := c.CaptureBound(ctx, target)
	if err != nil {
		return PointInspection{}, err
	}
	if json.Unmarshal([]byte(fresh), &now) != nil || now.Target != target.ID {
		return PointInspection{}, ErrTargetLost
	}
	if old.X != now.X || old.Y != now.Y || old.W != now.W || old.H != now.H {
		return PointInspection{}, staleReason("window_geometry_changed", "observe the moved window again")
	}
	c.mu.Lock()
	w, ok := c.windows[target.ID]
	pinned := c.pinned[target.ID]
	if c.closed || !ok || pinned == 0 {
		c.mu.Unlock()
		return PointInspection{}, ErrTargetLost
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return PointInspection{}, ErrUnavailable
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	var hit C.cu_probe
	bounds := C.CGRectMake(C.double(now.X), C.double(now.Y), C.double(now.W), C.double(now.H))
	p := C.CGPointMake(C.double(now.X+x*now.W), C.double(now.Y+y*now.H))
	checked := C.cu_probe_point(w, pinned, bounds, p, flag, &hit)
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.clearPointReviewLocked()
	mask, _ := ctx.Value(pointRegionsKey{}).(uint16)
	maskPoint := pointMask([2]float64{x, y})
	// Required regions are computed from the hit control, not chosen by the
	// model. context_tiles may add visible context but cannot remove them.
	required := uint16(hit.tiles) | maskPoint
	regions := required | mask
	role := C.GoString(&hit.role[0])
	same := sameRegions(old.Tiles, now.Tiles, regions)
	axBound := ctx.Err() == nil && axControlEligible(checked != 0, hit.visible != 0, hit.control != 0, old.Nonce != "", same, required)
	coordinate := checked != 0 && ctx.Err() == nil && hit.control == 0 &&
		coordinateFallbackEligible(checked != 0, role, hit.member != 0, hit.safe != 0, hit.visible != 0, old.Nonce != "", required, same)
	bound := axBound || coordinate
	if axBound {
		c.reviewed = macPointReview{nonce: old.Nonce, point: [2]float64{x, y}, regions: regions, control: hit.control}
	} else if coordinate {
		c.reviewed = macPointReview{nonce: old.Nonce, point: [2]float64{x, y}, regions: regions, coordinate: true}
	} else if hit.control != 0 {
		C.cu_ax_release(hit.control)
	}
	c.mu.Unlock()
	if err := c.Validate(ctx, target, false); err != nil {
		return PointInspection{}, err
	}
	if checked == 0 {
		return PointInspection{}, staleReason("probe_geometry_unverified", "could not verify the reviewed window geometry")
	}
	kind := ""
	if axBound {
		kind = "ax_control"
	} else if coordinate {
		kind = "coordinate"
	}
	verification := C.GoString(C.cu_point_verdict_name(hit.verdict))
	observation := PointInspection{InputBound: bound, InputKind: kind, Role: role, WindowVerified: hit.member != 0, NonSensitiveVerified: hit.safe != 0, EditableVerified: hit.editable != 0, PointUnobscured: hit.visible != 0, PointVerification: verification, PointTile: comparePointTile(old.Tiles, now.Tiles, [2]float64{x, y})}
	if observation.WindowVerified {
		observation.ControlBlock = C.GoString(&hit.block[0])
	}
	if coordinate {
		observation.Note = "coordinate_fallback: no verifiable AX control; graphical click, typing or key input is allowed at this point after the native checks. Verify the visible action context before sending; typed text and key presses cannot be verified by AX."
	} else if !bound {
		observation.Note = pointInspectionNote(observation)
	}
	if !observation.PointUnobscured {
		const obscured = "point_not_verified: the point could not be verified to reach the authorized window"
		if observation.Note == "" {
			observation.Note = obscured
		} else {
			observation.Note += "; " + obscured
		}
		if observation.PointVerification == "" {
			observation.PointVerification = "unverified"
		}
	}
	return observation, nil
}

// ClickFrame refuses unless the same window, frame geometry, focus, AX hit and
// absence of occluding windows can be established at the point of dispatch.
func (c *MacWindowCatalog) ClickFrame(ctx context.Context, target Target, token string, x, y float64) error {
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_click_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.double(x), C.double(y), flag, 0)
	}, [2]float64{x, y})
}

func (c *MacWindowCatalog) ScrollFrame(ctx context.Context, target Target, token string, x, y float64, dx, dy int) error {
	if dx < -5 || dx > 5 || dy < -5 || dy > 5 || (dx == 0 && dy == 0) {
		return ErrNotAllowed
	}
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_scroll_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.double(x), C.double(y), C.int(dx), C.int(dy), flag)
	}, [2]float64{x, y})
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

func (c *MacWindowCatalog) TypeTextFrame(ctx context.Context, target Target, token, text string) error {
	if !validFrameText(text) {
		return noInputError(ErrNotAllowed)
	}
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_type_text_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), value, C.size_t(len(text)), flag, 0)
	})
}

// TypeAtFrame brings only the exact target forward, clicks the captured point
// and types there. It is the fallback for web contenteditable surfaces with no
// AX text control; the previous Space and frontmost application are restored
// afterwards.
func (c *MacWindowCatalog) TypeAtFrame(ctx context.Context, target Target, token string, x, y float64, text string, submit bool) error {
	if text != "" && !validFrameText(text) {
		return noInputError(ErrNotAllowed)
	}
	if text == "" && !submit {
		return noInputError(ErrNotAllowed)
	}
	if err := c.Validate(ctx, target, false); err != nil {
		return err
	}
	var binding macFrameBinding
	if json.Unmarshal([]byte(token), &binding) != nil || binding.Target != target.ID {
		return noInputError(ErrStaleObservation)
	}
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	closed := c.closed
	c.mu.Unlock()
	if closed || !ok {
		return noInputError(ErrTargetLost)
	}
	previous := C.cu_frontmost_pid()
	space := C.cu_active_space()
	if c.homeSpace != 0 {
		space = c.homeSpace
	}
	if previous <= 0 || previous == C.int32_t(window.pid) {
		previous = c.homePID
	}
	if space == 0 {
		space = c.homeSpace
	}
	C.cu_activate_pid(C.int32_t(window.pid))
	time.Sleep(350 * time.Millisecond)
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	submitFlag := C.int(0)
	if submit {
		submitFlag = 1
	}
	err := c.sendFrame(ctx, target, binding, func(w C.cu_window, pinned C.AXUIElementRef, b macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_type_at_frame(w, pinned, C.double(b.X), C.double(b.Y), C.double(b.W), C.double(b.H), C.double(x), C.double(y), value, C.size_t(len(text)), flag, 1, submitFlag)
	})
	c.restoreAfterForeground(previous, space)
	return err
}

func (c *MacWindowCatalog) restoreAfterForeground(previous C.int32_t, space C.uint64_t) {
	if previous <= 0 && space == 0 {
		return
	}
	go func() {
		start := time.Now()
		stable := 0
		for time.Now().Before(start.Add(3 * time.Second)) {
			time.Sleep(150 * time.Millisecond)
			if previous > 0 && C.cu_frontmost_pid() != previous {
				C.cu_activate_pid(previous)
			}
			if space != 0 && C.cu_active_space() != space {
				C.cu_restore_space(space)
			}
			if (space == 0 || C.cu_active_space() == space) && (previous == 0 || C.cu_frontmost_pid() == previous) {
				stable++
				if stable >= 2 {
					break
				}
			} else {
				stable = 0
			}
		}
	}()
}

func (c *MacWindowCatalog) OperatorClickFrame(ctx context.Context, target Target, token string, x, y float64) error {
	if !isOperatorContext(ctx) {
		return noInputError(ErrNotAllowed)
	}
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_click_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), C.double(x), C.double(y), flag, 1)
	}, [2]float64{x, y})
}

func (c *MacWindowCatalog) OperatorTypeTextFrame(ctx context.Context, target Target, token, text string) error {
	if !isOperatorContext(ctx) || !validFrameText(text) {
		return noInputError(ErrNotAllowed)
	}
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	return c.dispatchFrame(ctx, target, token, func(window C.cu_window, pinned C.AXUIElementRef, binding macFrameBinding, flag *C.cu_cancel_flag) C.int {
		return C.cu_type_text_frame(window, pinned, C.double(binding.X), C.double(binding.Y), C.double(binding.W), C.double(binding.H), value, C.size_t(len(text)), flag, 1)
	})
}

// Keep the last identity check inside the same gate as dispatch. Only failures
// before send have a known no-input outcome; do not reclassify send's error.
func dispatchReviewedFrame(target Target, token string, capture func() (string, error), focus func() (bool, error), validate func() error, send func(macFrameBinding) error, regions uint16) error {
	var binding macFrameBinding
	if err := json.Unmarshal([]byte(token), &binding); err != nil {
		return noInputError(staleReason("frame_token_invalid", "invalid reviewed frame binding"))
	}
	if binding.Target != target.ID {
		return noInputError(staleReason("frame_target_mismatch", "reviewed frame does not bind this target"))
	}
	freshToken, err := capture()
	if err != nil {
		return noInputError(noInputValidationError(err))
	}
	var current macFrameBinding
	if json.Unmarshal([]byte(freshToken), &current) != nil || current.Target != target.ID {
		return noInputError(fmt.Errorf("%w: capture did not prove the pinned identity", ErrTargetLost))
	}
	if current.X != binding.X || current.Y != binding.Y || current.W != binding.W || current.H != binding.H {
		return noInputError(staleReason("window_geometry_changed", "the window moved or resized after the reviewed frame"))
	}
	if regions != 0 && !sameRegions(binding.Tiles, current.Tiles, regions) {
		return noInputError(staleReason("action_region_changed", "the reviewed target or action context changed; inspect before choosing another action"))
	}
	changed, err := focus()
	if err != nil {
		return noInputError(noInputValidationError(err))
	}
	if changed {
		return noInputError(staleReason("focus_restored", "the authorized window was brought forward; inspect its new frame before choosing an action"))
	}
	if err := validate(); err != nil {
		// Do not demote target loss (including its WindowDiagnostic) to stale.
		return noInputError(err)
	}
	return send(binding)
}

func (c *MacWindowCatalog) dispatchFrame(ctx context.Context, target Target, token string, dispatch func(C.cu_window, C.AXUIElementRef, macFrameBinding, *C.cu_cancel_flag) C.int, point ...[2]float64) error {
	var regions uint16
	if !isOperatorContext(ctx) {
		var b macFrameBinding
		c.mu.Lock()
		review := c.reviewed
		c.reviewed = macPointReview{}
		c.mu.Unlock()
		if review.control != 0 {
			defer C.cu_ax_release(review.control)
		}
		if json.Unmarshal([]byte(token), &b) != nil || b.Nonce == "" || b.Nonce != review.nonce || (review.control == 0 && !review.coordinate) || review.regions == 0 || (len(point) > 0 && point[0] != review.point) {
			return noInputError(staleReason("control_review_required", "use inspect_point on a fresh frame with the target and relevant context_tiles, or use a verified snapshot ref"))
		}
		regions = review.regions
		native := dispatch
		dispatch = func(w C.cu_window, pinned C.AXUIElementRef, b macFrameBinding, flag *C.cu_cancel_flag) C.int {
			if review.coordinate {
				flag.coordinate = 1
				flag.px = C.double(review.point[0])
				flag.py = C.double(review.point[1])
			} else {
				flag.reviewed = review.control
			}
			return native(w, pinned, b, flag)
		}
	} else if len(point) > 0 {
		regions = pointMask(point[0])
	}
	return dispatchReviewedFrame(target, token, func() (string, error) {
		_, current, err := c.CaptureBound(ctx, target)
		return current, err
	}, func() (bool, error) {
		return prepareInputFocus(ctx, 2*time.Second, 100*time.Millisecond,
			func() (bool, error) { return c.selectedFocus(ctx, target) },
			func() error { return c.focusSelected(ctx, target) })
	}, func() error { return c.Validate(ctx, target, true) }, func(binding macFrameBinding) error {
		return c.sendFrame(ctx, target, binding, dispatch)
	}, regions)
}

func (c *MacWindowCatalog) sendFrame(ctx context.Context, target Target, binding macFrameBinding, dispatch func(C.cu_window, C.AXUIElementRef, macFrameBinding, *C.cu_cancel_flag) C.int) error {
	c.mu.Lock()
	window, ok := c.windows[target.ID]
	if c.closed || !ok || c.pinned[target.ID] == 0 {
		c.mu.Unlock()
		return noInputError(ErrTargetLost)
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return noInputError(errors.New("allocate input cancellation flag"))
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	sent := dispatch(window, c.pinned[target.ID], binding, flag)
	refusal := flag.refusal
	verdict := C.GoString(C.cu_point_verdict_name(flag.point))
	if !stop() {
		<-done
	}
	C.free(unsafe.Pointer(flag))
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		if sent == 0 {
			return noInputError(err)
		}
		return err
	}
	if sent == 0 {
		// Native frame dispatchers return zero only before posting an
		// event. Recheck the same pinned window; never replay the input.
		check := func() error { return c.Validate(ctx, target, false) }
		switch refusal {
		case C.CU_INPUT_OCCLUDED:
			return inputRefused(ctx, check, staleReason("point_not_verified", "the reviewed point could not be verified to reach the authorized window ("+verdict+"); no input was sent"))
		case C.CU_INPUT_AX_HIT:
			return inputRefused(ctx, check, staleReason("ax_hit_unverified", "could not verify a safe direct AX control at the point"))
		case C.CU_INPUT_GEOMETRY:
			return inputRefused(ctx, check, staleReason("window_geometry_changed", "window geometry changed before dispatch"))
		default:
			return frameInputRefused(ctx, check)
		}
	}
	if sent < 0 {
		return errors.New("input was only partially dispatched; inspect the selected window before any new action; do not replay the text")
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
	return c.restoreSelected(ctx, target, false)
}

func (c *MacWindowCatalog) PrepareForeground(ctx context.Context, target Target) (bool, error) {
	return prepareInputFocus(ctx, 2*time.Second, 100*time.Millisecond,
		func() (bool, error) { return c.selectedFocus(ctx, target) },
		func() error { return c.restoreSelected(ctx, target, true) })
}

func (c *MacWindowCatalog) restoreSelected(ctx context.Context, target Target, allowTargetedRaise bool) error {
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
	operator := C.int(0)
	if isOperatorContext(ctx) {
		operator = 1
	}
	reason := C.cu_activation_reason(window, operator)
	// If app-wide activation risks bringing a different Space/window forward,
	// request AXRaise only on the retained window. macOS may decline it; never
	// activate the application as a fallback in that case.
	targeted := allowTargetedRaise && (reason == C.CU_FOCUS_NOT_VISIBLE || reason == C.CU_FOCUS_OFFSCREEN_PEER)
	if reason != C.CU_FOCUS_SAFE && !targeted {
		c.mu.Unlock()
		return macActivationError(int(reason))
	}
	flag := C.cu_cancel_new()
	if flag == nil {
		c.mu.Unlock()
		return errors.New("allocate focus cancellation flag")
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { C.cu_cancel_set(flag); close(done) })
	var raised C.int
	if targeted {
		raised = C.cu_raise_pinned(window, c.pinned[target.ID], flag)
		if raised != 0 {
			// WindowServer need not publish the new stacking order before
			// AXRaise returns. Poll the guard, not the activation operation.
			err := afterPinnedRaise(ctx, 400*time.Millisecond, 50*time.Millisecond, func() (bool, error) {
				switch C.cu_selected_focus(window, c.pinned[target.ID], flag) {
				case -1:
					return false, ErrStaleObservation
				case 1:
					return true, nil
				}
				reason = C.cu_activation_reason(window, 0)
				switch reason {
				case C.CU_FOCUS_NOT_VISIBLE, C.CU_FOCUS_OFFSCREEN_PEER:
					return false, nil
				case C.CU_FOCUS_SAFE:
					// Revalidates the guard and the retained AX identity before activation.
					raised = C.cu_focus_selected(window, c.pinned[target.ID], flag, 0)
					return true, nil
				default:
					return false, macActivationError(int(reason))
				}
			})
			if err != nil {
				if errors.Is(err, errRaiseNotVisible) {
					// A person requested automatic foregrounding. With no other
					// real window to choose between, one app activation may move
					// the exact retained window from another Space.
					if C.cu_selected_focus(window, c.pinned[target.ID], flag) == 0 && C.cu_focus_selected(window, c.pinned[target.ID], flag, 1) != 0 {
						raised = 1
						err = nil
					} else {
						err = macActivationError(int(reason))
					}
				}
				if err != nil {
					// Clean up exactly once. A successful fallback must reach
					// the shared cleanup below; cleaning up here and falling
					// through would double-stop and wait forever.
					if !stop() {
						<-done
					}
					C.free(unsafe.Pointer(flag))
					c.mu.Unlock()
					return err
				}
			}
		}
	} else {
		raised = C.cu_focus_selected(window, c.pinned[target.ID], flag, operator)
	}
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
