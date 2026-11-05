package computeruse

import (
	"strings"
	"testing"
)

func TestLinuxProcessStartPinsPIDAndHandlesCommandParentheses(t *testing.T) {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0], fields[19] = "S", "12345"
	stat := "42 (command with ) spaces) " + strings.Join(fields, " ")
	if start, err := linuxProcessStart(stat, 42); err != nil || start != 12345 {
		t.Fatalf("start=%d err=%v", start, err)
	}
	if _, err := linuxProcessStart(stat, 43); err == nil {
		t.Fatal("accepted a different process ID")
	}
	if _, err := linuxProcessStart("42 (short) S 0", 42); err == nil {
		t.Fatal("accepted a truncated process identity")
	}
}
