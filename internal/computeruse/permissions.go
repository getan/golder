package computeruse

// PermissionState is a non-prompting view of platform permission status. It
// cannot grant Golder authority to observe or control any particular target.
type PermissionState struct {
	ScreenCapture string
	Accessibility string
}
