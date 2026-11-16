package mobilebridge

import "strings"

// selectMobilePrompt refreshes host-generated prompts against the current tool
// catalog while preserving explicitly supplied and previously saved custom ones.
func selectMobilePrompt(explicit, saved, current string) string {
	if explicit != "" {
		return explicit
	}
	const managedPrefix = "You are golder, a coding agent running inside an Android app."
	if saved != "" && !strings.HasPrefix(saved, managedPrefix) {
		return saved
	}
	return current
}
