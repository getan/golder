package tui

import (
	"os"
	"strings"
)

func isSSHSession() bool {
	return os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
}

// Resolve once at construction, not on every frame. SSH's latency and bandwidth
// make decorative redraws costly even when the remote terminal supports them.
func noAnimationsFromEnv() bool {
	if isSSHSession() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_NO_ANIMATIONS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
