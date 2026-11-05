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
