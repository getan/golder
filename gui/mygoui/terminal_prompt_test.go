package mygoui

import (
	"strings"
	"testing"
)

func TestTerminalDescriptionRoutesGenericRequestsToBuiltinGUI(t *testing.T) {
	text := (&terminalTool{}).Description()
	for _, want := range []string{
		"Golder GUI's built-in terminal panel", "打开终端", "action=new", "by default",
		"not computer_request", "explicitly requests a system terminal", "list/focus",
		"ordinary noninteractive commands still use bash", "explicit host/network review",
		"PTY allocation is denied",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("terminal description missing %q", want)
		}
	}
}
