package agenttool

import (
	"fmt"

	"github.com/getan/golder/internal/execsess"
)

// processExitReason is shared by foreground bash and background status reports.
// A negative exit code has no normal process exit status: preserve Wait's real
// error if available, without guessing who sent a signal. Ordinary exit codes
// keep their established wording instead of repeating "exit status N".
func processExitReason(s execsess.Snapshot) string {
	if s.ExitCode < 0 && s.Err != "" {
		return s.Err
	}
	return fmt.Sprintf("code %d", s.ExitCode)
}
