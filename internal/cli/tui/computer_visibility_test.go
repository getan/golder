package tui

import (
	"strings"
	"testing"
)

func TestComputerNoWindowsExplainsHowToRetry(t *testing.T) {
	s := newRemoteTestSession(t)
	setup := newComputerSetup(s.header.ID)
	s.computerSetup = setup
	m := NewModel(Options{})
	m.session = s
	next, _ := m.computerTargetsReady(computerTargetsMsg{setup: setup})
	m = next.(Model)
	text := strings.Join(m.transcript.contentLines(), "\n")
	if !strings.Contains(text, "minimized or on another desktop") || !strings.Contains(text, "retry") {
		t.Fatalf("missing visibility guidance: %q", text)
	}
}
