package ui

import (
	"os"
	"strings"
)

// DebugEnabled reports whether diagnostic UI is enabled. It is the one switch
// for renderings that are useful to inspect but are not part of the normal
// transcript; the first is an allow verdict, which is a normal outcome rather
// than something the user needs to read on every call.
//
// GOLDER_DEBUG=1 (or true/on/yes) enables it; --debug sets the same variable at
// startup.
func DebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_DEBUG"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}
