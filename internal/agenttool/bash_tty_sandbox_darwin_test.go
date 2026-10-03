//go:build darwin

package agenttool

import (
	"runtime"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/seatbelt"
)

// A tty session must work through the sandbox runner: the PTY is opened by the
// parent (outside the sandbox) and the slave fds are inherited by the
// sandbox-exec profile, so interactive input still round-trips.
func TestTTYSessionUnderSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	if !seatbelt.Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	dir := t.TempDir()
	tool := &BashTool{Dir: dir, Sandbox: seatbelt.New(dir), ForceSandbox: true}
	poll := &WriteStdinTool{Sessions: tool.manager()}

	res := runToolArgs(t, tool, map[string]any{"command": `read -r x; echo "got:$x"`, "yield_time_ms": 300, "tty": true})
	id := resultDetails(t, res)["bash_id"].(string)
	if id == "" {
		t.Fatalf("no bash_id in %+v", resultDetails(t, res))
	}
	res = runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": "sandboxed\n", "yield_time_ms": 4000})
	if text := resultText(res); !strings.Contains(text, "got:sandboxed") {
		t.Fatalf("output = %q, want got:sandboxed through the sandbox", text)
	}
}
