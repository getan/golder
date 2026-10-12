package ui

// Path display helpers shared by the front-ends and the packages that render
// reports for them (trust, permissions). They live here rather than in a
// consumer because two of those consumers cannot import each other: the cli
// package imports trust, so a helper in cli would be unreachable from trust's own
// report, and copying it would let the two spellings of "~" drift apart.

import (
	"os"
	"strings"
)

// DisplayHomePath shortens a path under $HOME to "~/…" for display. The path
// itself is never rewritten — a grant, a decision, or a file handle always uses
// the full path; this is only what the user reads.
func DisplayHomePath(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
