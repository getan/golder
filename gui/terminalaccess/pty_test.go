package terminalaccess

import (
	"reflect"
	"testing"
)

func TestTerminalEnvironmentOverridesPresentationOnly(t *testing.T) {
	base := []string{"PATH=/bin", "TERM=dumb", "ColorTerm=old", "LANG=zh_CN.UTF-8"}
	got := terminalEnvironment(base)
	want := []string{"PATH=/bin", "LANG=zh_CN.UTF-8", "TERM=xterm-256color", "COLORTERM=truecolor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal environment = %q, want %q", got, want)
	}
	if base[1] != "TERM=dumb" {
		t.Fatal("input environment was mutated")
	}
}

func TestStartProcessCleansUpBeforePTYAllocation(t *testing.T) {
	cleaned := false
	_, err := StartProcess(ProcessOptions{Cleanup: func() { cleaned = true }})
	if err == nil || !cleaned {
		t.Fatalf("empty command: err=%v cleaned=%t", err, cleaned)
	}
}
