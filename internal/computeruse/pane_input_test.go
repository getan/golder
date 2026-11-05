package computeruse

import (
	"strings"
	"testing"
)

func TestPaneInputFilterIgnoresTerminalReplies(t *testing.T) {
	var filter paneInputFilter
	var visible strings.Builder
	for _, r := range "\x1b_Gi=31;resume\nstop\x1b\\observe\r" {
		if key, ok := filter.accept(r); ok {
			visible.WriteRune(key)
		}
	}
	if visible.String() != "observe\r" {
		t.Fatalf("terminal reply became input: %q", visible.String())
	}
}

func TestPaneInputFilterKeepsEmergencyPauseAndFailsClosed(t *testing.T) {
	var filter paneInputFilter
	for _, r := range "\x1b_Gpartial" {
		filter.accept(r)
	}
	if key, ok := filter.accept(0x07); !ok || key != 0x07 {
		t.Fatal("terminal metadata suppressed emergency pause")
	}
	for _, r := range "\x1b_Gpartial" {
		filter.accept(r)
	}
	if key, ok := filter.accept(0x03); !ok || key != 0x03 {
		t.Fatal("terminal metadata suppressed emergency stop")
	}
	for _, r := range "\x1b_G" + strings.Repeat("x", 4097) {
		if key, ok := filter.accept(r); ok && key == 0x03 {
			return
		}
	}
	t.Fatal("overlong terminal metadata did not fail closed")
}
