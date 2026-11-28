//go:build windows

package proc

import (
	"context"
	"testing"
)

func TestCommandHidesConsole(t *testing.T) {
	cmd := Command("cmd.exe", "/d", "/s", "/c", "exit 0")
	if cmd.SysProcAttr == nil {
		t.Fatal("Command left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("Command did not set HideWindow")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("Command did not set CREATE_NO_WINDOW")
	}
}

func TestCommandContextHidesConsole(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", "exit 0")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CommandContext did not set hidden-console attributes: %+v", cmd.SysProcAttr)
	}
}
