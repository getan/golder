package agenttool

import (
	"strings"
	"testing"
)

func TestComputerRequestDefersGenericTerminalRequestsWhenBuiltinAvailable(t *testing.T) {
	text := (&ComputerRequestTool{}).Description()
	for _, want := range []string{
		"If a built-in terminal tool is available", "generic requests to open a terminal use that tool, not Computer Use",
		"explicitly requests it", "subject to target restrictions", "End the turn", "Use browser for websites",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("computer_request description missing %q", want)
		}
	}
}
