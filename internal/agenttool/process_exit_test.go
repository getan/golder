package agenttool

import (
	"testing"
	"time"

	"github.com/getan/golder/internal/execsess"
)

func TestProcessExitReasonAndSessionStatus(t *testing.T) {
	for _, tc := range []struct {
		name, err, reason, status string
		code                      int
	}{
		{"success", "", "code 0", "[bash_test: exited code 0]", 0},
		{"normal failure", "exit status 3", "code 3", "[bash_test: exited code 3: exit status 3]", 3},
		{"signal", "signal: killed", "signal: killed", "[bash_test: exited: signal: killed]", -1},
		{"unknown status", "", "code -1", "[bash_test: exited code -1]", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := execsess.Snapshot{ID: "bash_test", Status: execsess.StatusExited, ExitCode: tc.code, Err: tc.err}
			if got := processExitReason(s); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
			if got := sessionStatusLine(s); got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
		})
	}
	s := execsess.Snapshot{ID: "bash_test", Status: execsess.StatusExited, ExitCode: -1, Err: "signal: killed", TimedOut: true, Timeout: time.Second}
	if got := sessionStatusLine(s); got != "[bash_test: exited: timed out after 1s]" {
		t.Fatalf("timeout classification changed: %q", got)
	}
}
