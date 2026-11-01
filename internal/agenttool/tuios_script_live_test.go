package agenttool

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
)

// A disposable real tuios daemon, real PTYs and ordinary /bin/sh panes. Startup
// readiness is its JSON acknowledgement, never a sleep/socket polling loop.
func TestTuiosScriptLiveMini(t *testing.T) {
	bin := os.Getenv("GOLDER_TEST_TUIOS_MINI")
	if bin == "" {
		t.Skip("set GOLDER_TEST_TUIOS_MINI to a tuios-mini binary")
	}
	dir, err := os.MkdirTemp("", "gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	socket := filepath.Join(dir, "d.sock")
	cmd := exec.CommandContext(ctx, bin, "--socket", socket, "--session", "wire", "--shell", "/bin/sh", "--cwd", dir, "--log", filepath.Join(dir, "daemon.log"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	var ready map[string]any
	if err = json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatal("daemon readiness", err)
	}
	go io.Copy(io.Discard, stdout)
	options := tuiosctl.Options{Socket: socket, Session: "wire", Version: "golder-script-live"}
	c, err := tuiosctl.Dial(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pane, err := c.NewWindow(ctx, tuiosctl.NewWindowParams{Name: "zero-install", Command: []string{"/bin/sh"}, Focus: false})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseWindow(context.Background(), pane.WindowID)
	if _, err = c.ResizeWindow(ctx, pane.WindowID, 24, 24); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUIOS_SESSION", "wire")
	t.Setenv("TUIOS_PANE_ID", "test-agent")
	tool := NewTuiosTool(dir, true)
	trustFakeControl(tool.Control)
	tool.newClient = func(ctx context.Context) (*tuiosctl.Client, error) { return tuiosctl.Dial(ctx, options) }
	path := filepath.Join(dir, "verified.txt")
	content := strings.Repeat("中文-file-content\n", 4000)
	script := "printf %s " + quoteControlArg(content) + " > " + quoteControlArg(path) + "\nprintf 'LIVE-SCRIPT-OK\\n'\n"
	raw, _ := json.Marshal(map[string]any{"action": "run", "window": pane.WindowID, "script": script, "timeout_ms": 15000})
	result, err := tool.Execute(ctx, "live-script", raw, nil)
	if err != nil || result.IsError {
		t.Fatalf("real pane: %v %s", err, agentcore.ContentToText(result.Content))
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("file changed/truncated: bytes=%d want=%d err=%v", len(data), len(content), err)
	}
	t.Logf("real daemon+PTY: %d UTF-8 bytes verified before execution at 24 columns; no remote installation, no capture polling", len(data))
}
