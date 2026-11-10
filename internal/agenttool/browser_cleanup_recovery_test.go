package agenttool

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
)

func TestBrowserExitedPaneRecoveryGuidanceIsConsistentAcrossTools(t *testing.T) {
	ConfigureNativeBrowserLauncher(nil)
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })
	browser := NewBrowserTool(t.TempDir(), true)
	for name, hint := range map[string]string{
		"browser description":   browser.Description(),
		"quit result":           browser.quitPendingHint(),
		"tuios description":     (&TuiosTool{allowControl: true}).Description(),
		"workspace description": (&WorkspaceTool{allowControl: true}).Description(),
	} {
		for _, required := range []string{
			"browser quit was already sent prematurely", "browser list", "recorded browserId or socket",
			"must not be truncated", "probe timeouts or permission errors do not prove absence",
			"controlled=true", "control_origin=agent-created", "own=false", "full window id",
			"condition=window-exit", "matched=true", "re-list", "no foreground process",
			"empty foreground_cmd or idle output alone is not proof of exit", "exactly one tuios close force:true",
			"already gone", "Do not repeat quit or retry force", "operator-claimed", "paused browser",
			"stop and report", "does not prove that shared Chromium or its profile has exited",
		} {
			if !strings.Contains(hint, required) {
				t.Errorf("%s lacks recovery requirement %q: %s", name, required, hint)
			}
		}
	}
	if hint := browser.cleanupHint(false); !strings.Contains(hint, "do not issue browser quit first") || !strings.Contains(hint, "Never use force automatically except") {
		t.Fatalf("recovery obscured the preferred normal close path: %s", hint)
	}
}

func TestBrowserExitedPaneRecoveryDoesNotLeakIntoGUIOrReadOnlyHints(t *testing.T) {
	browser := NewBrowserTool(t.TempDir(), true)
	ConfigureNativeBrowserLauncher(func(context.Context, NativeBrowserRequest) error { return nil })
	t.Cleanup(func() { ConfigureNativeBrowserLauncher(nil) })
	for _, hint := range []string{
		browser.cleanupHint(true), browser.quitPendingHint(), browser.Description(),
		NewBrowserTool(t.TempDir(), false).Description(),
		(&TuiosTool{allowControl: false}).Description(), (&WorkspaceTool{allowControl: false}).Description(),
	} {
		if strings.Contains(hint, "exited-pane recovery") || strings.Contains(hint, "exactly one tuios close force:true") {
			t.Fatalf("TUI force recovery leaked to a GUI/read-only surface: %s", hint)
		}
	}
}

func TestClosePanePrematureBrowserExitStillRequiresExplicitForce(t *testing.T) {
	root := shortRoot(t)
	pane := managedPane(t, root, "browser-id", filepath.Join(root, "already-gone.sock"), "browser-1", true)
	win := embeddedBrowserWindow()
	win.ForegroundCmd = ""
	win.Controlled, win.ControlOrigin = true, "agent-created"
	zero := 0
	f := &paneCloseFake{windows: []tuiosctl.Window{win}, result: tuiosctl.WaitResult{Matched: true, ExitCode: &zero}}
	// A missing endpoint plus empty foreground still cannot implicitly force.
	if _, err := closePane(context.Background(), f, win, pane, false, time.Second); err == nil || len(f.calls) != 0 {
		t.Fatalf("normal close guessed that an unreachable browser was safe to force: calls=%v err=%v", f.calls, err)
	}
	// The recovery workflow separately observes the exact pane's exit event,
	// then explicitly requests one force call. The backend sends no keys/quit.
	if _, err := f.WaitFor(context.Background(), tuiosctl.WaitParams{Condition: "window-exit", Window: win.WindowID}); err != nil {
		t.Fatal(err)
	}
	out, err := closePane(context.Background(), f, win, pane, true, time.Second)
	if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"wait:browser-id:window-exit", "close:browser-id"}) {
		t.Fatalf("explicit cleanup did more than remove the exact pane: calls=%v out=%v err=%v", f.calls, out, err)
	}
	if _, ok := managedBrowserPane(root, win.WindowID); ok {
		t.Fatal("closed empty pane retained its browser record")
	}
}
