//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/getan/golder/internal/browserctl"
)

// Unlike fill, these events travel through the pane's real terminal input,
// pointer mapping, chrome focus and page keyboard gate. The API only reads the
// result. Opt-in because this needs both a PTY and an installed Chromium.
func TestBrowserManualInputPTY(t *testing.T) {
	if os.Getenv("GOLDER_BROWSER_INPUT_CHILD") == "1" {
		os.Exit(runBrowse([]string{
			"-private", "-chrome", os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE"),
			"-control-socket", os.Getenv("GOLDER_BROWSER_INPUT_SOCKET"),
			"-measure-cells=false", "-fps", "4", os.Getenv("GOLDER_BROWSER_INPUT_URL"),
		}, os.Stdin, os.Stdout, os.Stderr))
	}
	if os.Getenv("GOLDER_TEST_BROWSER_INPUT") != "1" || os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE") == "" {
		t.Skip("set GOLDER_TEST_BROWSER_INPUT=1 and GOLDER_TEST_CHROMIUM_EXECUTABLE")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Manual input fixture</title><body style="margin:0">
<input id="plain" style="position:absolute;left:20px;top:20px;width:300px;height:60px">
<div id="host" style="position:absolute;left:20px;top:120px"></div>
<output id="result" style="position:absolute;top:250px"></output>
<button id="toggle" style="position:absolute;left:20px;top:220px;width:300px;height:60px" onclick="const g=document.querySelector('#gate');g.hidden=!g.hidden">toggle fixture verification</button>
<div id="gate" role="dialog" hidden style="position:absolute;left:20px;top:330px">操作过快，请慢一点[5014][502]</div>
<script>
document.querySelector('#host').attachShadow({mode:'open'}).innerHTML =
  '<input style="width:300px;height:60px">';
document.addEventListener('input', e => {
  document.querySelector('#result').textContent = e.composedPath()[0].value;
});
</script>`)
	}))
	defer server.Close()
	// The socket must fit Darwin's 104-byte sockaddr_un even with Go's long
	// per-test directory names.
	root, err := os.MkdirTemp("", "gbin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "c.sock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestBrowserManualInputPTY$")
	for _, env := range os.Environ() {
		// A fixture browser must not rename or otherwise report into the live
		// workspace pane that happens to be running this test.
		if !strings.HasPrefix(env, "TUIOS_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GOLDER_BROWSER_INPUT_CHILD=1", "TERM=xterm-kitty",
		"KITTY_WINDOW_ID=1", "GOLDER_BROWSER_INPUT_SOCKET="+socket,
		"GOLDER_BROWSER_INPUT_URL="+server.URL)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 32, Cols: 100, X: 1000, Y: 640})
	if err != nil {
		t.Fatalf("PTY unavailable; manual input was not exercised: %v", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		cancel()
		_ = terminal.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("browser fixture did not terminate")
		}
	}()
	go func() { _, _ = io.Copy(io.Discard, terminal) }()
	c := browserctl.NewWithTransport(socket, nil)
	defer c.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := c.Info(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("browser pane never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	assertResult := func(t *testing.T, want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		var got string
		var err error
		for {
			var raw json.RawMessage
			raw, _, err = c.Action(ctx, "get", map[string]any{"kind": "text", "selector": "#result"}, browserctl.Target{}, time.Second)
			if err == nil {
				err = json.Unmarshal(raw, &got)
			}
			if err == nil && got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("manual terminal input: got=%q want=%q err=%v", got, want, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, tc := range []struct {
		name, text string
		row        int
	}{
		{"address-to-page", "a中文", 5},
		{"shadow-input", "b中文", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Ctrl+L deliberately leaves the toolbar editing. Clicking the page
			// must return keyboard ownership to the field without an extra Esc.
			if _, err := fmt.Fprintf(terminal, "\x0c\x1b[<0;9;%dM\x1b[<0;9;%dm%s", tc.row, tc.row, tc.text); err != nil {
				t.Fatal(err)
			}
			assertResult(t, tc.text)
			if _, err := terminal.Write([]byte{0x7f}); err != nil {
				t.Fatal(err)
			}
			assertResult(t, strings.TrimSuffix(tc.text, "文"))
		})
	}
	t.Run("manual-verification-and-resume", func(t *testing.T) {
		waitPaused := func(want bool) {
			t.Helper()
			deadline := time.Now().Add(3 * time.Second)
			for {
				raw, _, err := c.Action(ctx, "interaction.status", map[string]any{}, browserctl.Target{}, time.Second)
				var state struct {
					Paused bool `json:"paused"`
				}
				if err == nil && json.Unmarshal(raw, &state) == nil && state.Paused == want {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("pause state did not become %v: %v", want, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		clickToggle := func() {
			t.Helper()
			if _, err := io.WriteString(terminal, "\x1b[<0;9;15M\x1b[<0;9;15m"); err != nil {
				t.Fatal(err)
			}
		}
		blocked := func() {
			t.Helper()
			_, _, err := c.Action(ctx, "focus", map[string]any{"selector": "#plain"}, browserctl.Target{}, time.Second)
			if err == nil || !strings.Contains(err.Error(), "browser_paused") {
				t.Fatalf("automatic page action was not paused: %v", err)
			}
		}
		clickToggle()
		waitPaused(true)
		blocked()
		// Manual pointer input still works while automatic actions are paused.
		clickToggle()
		// Wait until the actual page says the manually hidden dialog is hidden.
		deadline := time.Now().Add(3 * time.Second)
		for {
			raw, _, err := c.Action(ctx, "is", map[string]any{"kind": "visible", "selector": "#gate"}, browserctl.Target{}, time.Second)
			if err == nil && string(raw) == "false" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("manual input could not dismiss the fixture challenge")
			}
			time.Sleep(20 * time.Millisecond)
		}
		waitPaused(true)
		blocked()
		if _, err := terminal.Write([]byte{0x07}); err != nil {
			t.Fatal(err)
		}
		waitPaused(false)
		if _, _, err := c.Action(ctx, "focus", map[string]any{"selector": "#plain"}, browserctl.Target{}, time.Second); err != nil {
			t.Fatal(err)
		}
	})
	if err := c.Quit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if waitErr != nil {
			t.Fatalf("browser pane did not quit cleanly: %v", waitErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("browser pane did not acknowledge normal quit")
	}
	if !t.Failed() {
		t.Log("real pane: address-to-page focus, ASCII/CJK input, shadow input, Backspace and normal quit verified")
	}
}
