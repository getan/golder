package computeruse

import "fmt"

// WindowDiagnostic identifies a failed macOS window identity check without
// exposing window titles, screen contents, or accessibility element values.
type WindowDiagnostic struct {
	Reason   WindowFailure
	AXError  int
	Windows  int
	Readable int
	Matches  int
}

type WindowFailure string

const (
	WindowPermissionDenied WindowFailure = "accessibility-denied"
	WindowUnavailable      WindowFailure = "window-unavailable"
	WindowBoundsMissing    WindowFailure = "screen-bounds-unavailable"
	WindowAXAppUnavailable WindowFailure = "ax-application-unavailable"
	WindowAXDisabled       WindowFailure = "ax-api-disabled"
	WindowAXReadFailed     WindowFailure = "ax-windows-read-failed"
	WindowAXNoWindows      WindowFailure = "ax-no-windows"
	WindowAXGeometry       WindowFailure = "ax-geometry-unavailable"
	WindowAXNoMatch        WindowFailure = "ax-geometry-mismatch"
	WindowAXAmbiguous      WindowFailure = "ax-ambiguous-match"
	WindowAXLimit          WindowFailure = "ax-window-limit"
	WindowAXFocus          WindowFailure = "ax-focus-changed"
)

// Unwrap keeps the existing fail-closed target-loss contract for callers that
// inspect errors directly rather than through Session.
func (d *WindowDiagnostic) Unwrap() error { return ErrTargetLost }

func (d *WindowDiagnostic) Error() string {
	if d == nil {
		return "macOS window validation failed"
	}
	switch d.Reason {
	case WindowPermissionDenied:
		return "macOS Accessibility permission is not granted to this Golder process; check Privacy & Security → Accessibility"
	case WindowUnavailable:
		return "could not verify that the selected window remains visible and belongs to the same process; show the intended window and select it again"
	case WindowBoundsMissing:
		return "macOS did not provide bounds for the selected screen window; select it again when it is visible"
	case WindowAXAppUnavailable:
		return "macOS could not create an Accessibility reference for the selected application"
	case WindowAXDisabled:
		return fmt.Sprintf("macOS reports that the Accessibility API is disabled (AX error %d); check this process's permissions and host restrictions", d.AXError)
	case WindowAXReadFailed:
		return fmt.Sprintf("macOS could not read the application's Accessibility window list (AX error %d); the application may be busy or unavailable", d.AXError)
	case WindowAXNoWindows:
		return "macOS returned no Accessibility windows for the selected application; show the intended window and select it again"
	case WindowAXGeometry:
		return fmt.Sprintf("macOS could not read geometry for all Accessibility windows (%d of %d readable); the target could not be identified safely", d.Readable, d.Windows)
	case WindowAXNoMatch:
		return fmt.Sprintf("none of the %d Accessibility windows matched the selected screen window's position and size (tolerance: less than 2 points); the target could not be identified safely", d.Windows)
	case WindowAXAmbiguous:
		return fmt.Sprintf("%d Accessibility windows matched the selected screen window; the target is ambiguous and no input was sent", d.Matches)
	case WindowAXLimit:
		return fmt.Sprintf("the application has %d Accessibility windows, exceeding the 128-window identity-check limit", d.Windows)
	case WindowAXFocus:
		return "the selected application or window no longer has the required focus; no input was sent"
	default:
		return "macOS could not verify the selected window identity; no input was sent"
	}
}

func permissionLabel(value string) string {
	switch value {
	case "granted", "denied", "unknown":
		return value
	default:
		return "unknown"
	}
}

// DescribePermissions reports only this process's OS grants. They are separate
// from the operator's authorization for any particular window.
func DescribePermissions(p PermissionState) string {
	return fmt.Sprintf("System permissions for this Golder process: Screen Recording: %s; Accessibility: %s. These permissions do not authorize a target window.",
		permissionLabel(p.ScreenCapture), permissionLabel(p.Accessibility))
}

// DescribeFailure keeps the underlying failure distinct from permission state;
// a granted OS permission does not prove that a window was safely matched.
func DescribeFailure(err error, p PermissionState) string {
	if err == nil {
		return ""
	}
	return err.Error() + "\n" + DescribePermissions(p)
}
