package agenttool

import (
	"context"
	"strings"
	"testing"
)

func TestBrowserCleanupGuidanceDistinguishesTUIAndGUI(t *testing.T) {
	tool := NewBrowserTool(t.TempDir(), true)
	tui := tool.cleanupHint(false)
	for _, phrase := range []string{"newly started", "wants it left open", "paused for verification", "tuios close", "waits for process exit", "Never use force automatically"} {
		if !strings.Contains(tui, phrase) {
			t.Fatalf("TUI cleanup omits %q: %s", phrase, tui)
		}
	}
	gui := tool.cleanupHint(true)
	for _, phrase := range []string{"exact owned view", "not a tuios pane", "exit_confirmed is false", "does not prove"} {
		if !strings.Contains(gui, phrase) {
			t.Fatalf("GUI cleanup omits %q: %s", phrase, gui)
		}
	}
	tool.allowControl = false
	if got := tool.cleanupHint(false); !strings.Contains(got, "parent agent") || strings.Contains(got, "tuios close") {
		t.Fatalf("read-only sub-agent got control instructions: %s", got)
	}
}

func TestBrowserQuitPendingGuidanceMatchesFrontend(t *testing.T) {
	tool := NewBrowserTool(t.TempDir(), true)
	ConfigureNativeBrowserLauncher(nil)
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })
	if got := tool.quitPendingHint(); !strings.Contains(got, "tuios list/wait") || !strings.Contains(got, "not confirmed") {
		t.Fatalf("TUI quit hint = %q", got)
	}
	if got := tool.Description(); !strings.Contains(got, "tuios close") || !strings.Contains(got, "pane is gone") {
		t.Fatalf("TUI tool description does not expose cleanup guidance: %q", got)
	}
	ConfigureNativeBrowserLauncher(func(context.Context, NativeBrowserRequest) error { return nil })
	if got := tool.quitPendingHint(); strings.Contains(got, "tuios") || !strings.Contains(got, "does not prove shared Chromium") {
		t.Fatalf("GUI quit hint = %q", got)
	}
	if got := tool.Description(); !strings.Contains(got, "not a tuios pane") || !strings.Contains(got, "exit_confirmed is false") {
		t.Fatalf("GUI tool description does not expose cleanup guidance: %q", got)
	}
}
