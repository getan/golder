package tuiosctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveMiniDaemonContract is the bridge between the two repositories: it
// runs golder's own client against a real tuios-mini daemon. It is skipped
// unless tuios-mini's scripts/check-golder.sh provides a socket, so ordinary
// `go test ./...` never needs a daemon.
func TestLiveMiniDaemonContract(t *testing.T) {
	socket := os.Getenv("GOLDER_TUIOS_LIVE_SOCKET")
	if socket == "" {
		t.Skip("set GOLDER_TUIOS_LIVE_SOCKET to a running tuios-mini daemon (see tuios-mini/scripts/check-golder.sh)")
	}
	sessionName := os.Getenv("GOLDER_TUIOS_LIVE_SESSION")
	if sessionName == "" {
		sessionName = "work"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, Options{Socket: socket, Session: sessionName, Version: "golder-live-mini"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wl, err := c.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wl.Windows) == 0 {
		t.Fatal("the mini session has no windows")
	}
	first := wl.Windows[0].WindowID

	if _, err := c.GetWindow(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := c.NewWindow(ctx, NewWindowParams{
		Name: "live", Command: []string{"/bin/sh", "-c", "sleep 30"}, Focus: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.SendText(ctx, first, "printf 'LIVE-OK\\n'", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitFor(ctx, WaitParams{
		Condition: "window-output", Window: first, Pattern: "LIVE-OK", Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	captured, err := c.CapturePane(ctx, first, "visible", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured.Content, "LIVE-OK") {
		t.Fatalf("capture-pane = %q, want LIVE-OK", captured.Content)
	}

	ran := runAtPrompt(ctx, t, c, first, "echo LIVE-RUN")
	if ran.ExitCode == nil || *ran.ExitCode != 0 || !strings.Contains(ran.Output, "LIVE-RUN") {
		t.Fatalf("run = exit %v output %q", ran.ExitCode, ran.Output)
	}

	if _, err := c.FocusWindow(ctx, second.WindowID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MoveWindow(ctx, second.WindowID, 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResizeWindow(ctx, second.WindowID, 90, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RenameWindow(ctx, second.WindowID, "renamed"); err != nil {
		t.Fatal(err)
	}

	shot, err := c.Screenshot(ctx, ScreenshotParams{
		Window: first, Format: "png", Frame: "plain", Out: filepath.Join(t.TempDir(), "live.png"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(shot.Path); err != nil || info.Size() == 0 {
		t.Fatalf("screenshot file: info=%v err=%v", info, err)
	}

	if err := c.SendKeys(ctx, second.WindowID, "ctrl+c", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitFor(ctx, WaitParams{
		Condition: "window-exit", Window: second.WindowID, Timeout: 5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	// A process outside a pane cannot prove scope own (that needs the pane
	// token), so this exercises the same restriction with scope all. Golder's
	// production connections use scope own plus the token.
	restricted, err := Dial(ctx, Options{Socket: socket, Session: sessionName, Version: "golder-live-mini-restricted"})
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	if _, err := restricted.Call(ctx, "restrict-connection", map[string]any{"scope": "all", "read_only": true}); err != nil {
		t.Fatal(err)
	}
	_, err = restricted.SendText(ctx, first, "must-not-land", false)
	var de *Error
	if !errors.As(err, &de) || de.Code != "forbidden" {
		t.Fatalf("send-text on a read-only connection = %v, want forbidden", err)
	}

	if err := c.CloseWindow(ctx, second.WindowID); err != nil {
		t.Fatal(err)
	}
	wl, err = c.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wl.Windows) != 1 {
		t.Fatalf("windows after close = %d, want 1", len(wl.Windows))
	}
}

// runAtPrompt retries the one race the test cannot avoid: the prompt mark that
// follows a finished command is processed asynchronously.
func runAtPrompt(ctx context.Context, t *testing.T, c *Client, window, command string) RunResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, err := c.Run(ctx, window, command, 5*time.Second, 20)
		if err == nil {
			return res
		}
		var de *Error
		if !errors.As(err, &de) || de.Code != "not_at_prompt" || time.Now().After(deadline) {
			t.Fatalf("run: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
