//go:build !darwin || !cgo

package computeruse

// ProbePermissions deliberately does not imply that a system permission or
// desktop session exists on platforms without a supported probe yet.
func ProbePermissions() PermissionState {
	return PermissionState{ScreenCapture: "unknown", Accessibility: "unknown"}
}

func RequestMacPermission(string) bool { return false }
