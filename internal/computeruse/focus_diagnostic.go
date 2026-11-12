package computeruse

import "fmt"

// Native values mirror CU_FOCUS_*; diagnostics never contain other windows'
// names, coordinates or content.
func macActivationError(reason int) error {
	var message string
	switch reason {
	case 1:
		message = "the selected window is not visible on the current macOS desktop; capture can work across Spaces, but coordinate input cannot; show this same window on the current desktop"
	case 2:
		message = "macOS could not enumerate windows to check whether activation would change desktops"
	case 3:
		message = "macOS could not verify another real window of the selected application"
	case 4:
		message = "the selected application has another real window that is off-screen; activation could select a different desktop"
	case 5:
		message = "the application has multiple real windows; automatic activation cannot prove which desktop will be selected; show the selected window first"
	default:
		message = "macOS could not verify safe activation of the selected window"
	}
	return noInputError(fmt.Errorf("%w: %s; no input was sent", ErrStaleObservation, message))
}
