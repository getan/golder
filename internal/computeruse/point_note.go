package computeruse

// Classification only. The note never contains AX values or other windows'
// content, and a role name is metadata rather than user text.
func pointInspectionNote(observation PointInspection) string {
	if observation.InputBound {
		return ""
	}
	switch {
	case observation.Role == "":
		return "no_ax_hit: macOS returned no accessible control at this point"
	case !observation.WindowVerified:
		return "not_verified_for_this_window: the hit could not be proven to belong to the authorized window"
	case !observation.NonSensitiveVerified:
		return "sensitive_or_unknown: the hit may be a secure or unknown control and is never used for input"
	case observation.Role == "AXGroup" || observation.Role == "AXWebArea":
		return "container_not_bound: no verifiable AX control at this point and the graphical fallback was not granted; resolve any reported occlusion or region change, then inspect again to obtain the coordinate fallback, or choose another point"
	case observation.Role == "AXTextField" || observation.Role == "AXTextArea":
		return "text_control_not_editable: a text role was found but it is not settable; verify whether the app exposes another field"
	default:
		return "no_reviewable_control: the hit is not a supported non-sensitive control"
	}
}

// A reviewed AX control is not actionable until the point is also verified
// unobscured; dispatch repeats these checks immediately before input.
func axControlEligible(checked, visible, control, nonce, same bool, required uint16) bool {
	return checked && visible && control && nonce && required != 0 && same
}

// Coordinate fallback is allowed only for a point that is still inside the
// verified, non-sensitive target window and still unobscured. A foreign or
// sensitive hit never qualifies, even when the container itself is visible.
func coordinateFallbackEligible(checked bool, role string, member, safe, visible, hasNonce bool, required uint16, same bool) bool {
	if !checked || !visible || !hasNonce || required == 0 || !same {
		return false
	}
	return (member || role == "") && (safe || role == "")
}
