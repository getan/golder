package computeruse

// Native values mirror CU_FOCUS_*; diagnostics never contain other windows'
// names, coordinates or content.
func macActivationError(reason int) error {
	var code, message string
	switch reason {
	case 1:
		code = "window_off_desktop"
		message = "the selected window is not visible on the current macOS desktop or its visibility could not be verified; use a verified semantic control, restore the window from the Dock, or use Mission Control to bring that same window here before observing again"
	case 2:
		code = "focus_unavailable"
		message = "macOS could not enumerate windows to check whether activation would change desktops"
	case 3:
		code = "activation_peer_unverified"
		message = "macOS could not verify another real window of the selected application"
	case 4:
		code = "window_off_desktop"
		message = "the selected application has another real window that is off-screen; activation could select a different desktop"
	case 5:
		code = "activation_ambiguous"
		message = "the application has multiple real windows; automatic activation cannot prove which desktop will be selected; show the selected window first"
	default:
		code = "focus_unavailable"
		message = "macOS could not verify safe activation of the selected window"
	}
	return noInputError(staleReason(code, message+"; no input was sent"))
}
