package agenttool

import (
	"fmt"
	"strings"
)

// shellArgvPreview is the bounded, quoted argv shown in a failed call. The
// command is intentionally included: the model's tool arguments are already
// visible, and the exact argument vector is what distinguishes a quoting bug
// from a shell that never started.
func shellArgvPreview(argv []string) string {
	const max = 600
	s := fmt.Sprintf("%q", argv)
	if len(s) <= max {
		return s
	}
	s = strings.ToValidUTF8(s[:max], "")
	return s + " ...]"
}

// shellTraceLine is the one-line diagnostic appended to a failed command. It
// answers the questions the old output could not: which interpreter ran, with
// which argv, in which directory, and what the process returned.
func shellTraceLine(shell shellSpec, argv []string, dir string, exit int) string {
	return fmt.Sprintf("[shell=%q argv=%s cwd=%q exit=%d]", shell.path, shellArgvPreview(argv), dir, exit)
}
