//go:build darwin && cgo

package computeruse

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>

static int computeruse_accessibility_allowed(void) {
	return AXIsProcessTrusted();
}

static int computeruse_capture_allowed(void) {
	return CGPreflightScreenCaptureAccess();
}

static int computeruse_request_accessibility(void) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { kCFBooleanTrue };
	CFDictionaryRef options = CFDictionaryCreate(NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (!options) return 0;
	int trusted = AXIsProcessTrustedWithOptions(options);
	CFRelease(options);
	return trusted;
}

static int computeruse_request_capture(void) {
	return CGRequestScreenCaptureAccess();
}
*/
import "C"

// ProbePermissions reads the current OS grants without prompting for them.
// Which process must hold these permissions depends on the eventual driver.
func ProbePermissions() PermissionState {
	result := PermissionState{ScreenCapture: "denied", Accessibility: "denied"}
	if C.computeruse_capture_allowed() != 0 {
		result.ScreenCapture = "granted"
	}
	if C.computeruse_accessibility_allowed() != 0 {
		result.Accessibility = "granted"
	}
	return result
}

// RequestMacPermission is called only after an explicit operator button click.
func RequestMacPermission(kind string) bool {
	switch kind {
	case "accessibility":
		return C.computeruse_request_accessibility() != 0
	case "screen":
		return C.computeruse_request_capture() != 0
	default:
		return false
	}
}
