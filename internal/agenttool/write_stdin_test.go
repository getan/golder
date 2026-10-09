// Tests for the session flow: a long-running bash call hands back a bash_id,
// write_stdin drains its incremental output, reports exit codes, and interrupts
// the process group.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/execsess"
)

func runToolArgs(t *testing.T, tool agentcore.AgentTool, args map[string]any) agentcore.AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, execErr := tool.Execute(context.Background(), "call-1", raw, nil)
	if execErr != nil {
		t.Fatalf("%s: %v", tool.Name(), execErr)
	}
	return res
}

func resultDetails(t *testing.T, res agentcore.AgentToolResult) map[string]any {
	t.Helper()
	details, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %#v, want map", res.Details)
	}
	return details
}

func TestBashRunningSessionThenInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	sessions := execsess.NewManager()
	bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
	poll := &WriteStdinTool{Sessions: sessions}

	res := runToolArgs(t, bash, map[string]any{"command": "echo start; sleep 30", "yield_time_ms": 400})
	details := resultDetails(t, res)
	id, _ := details["bash_id"].(string)
	if id == "" || details["status"] != string(execsess.StatusRunning) {
		t.Fatalf("bash result = %+v, want a running session with bash_id", details)
	}
	if text := resultText(res); !strings.Contains(text, "start") || !strings.Contains(text, "running") {
		t.Fatalf("bash text = %q, want start + running", text)
	}
	// The interrupt hint is read by humans as well as the model: it must keep
	// the literal escape the model sends and spell out what that key is.
	if text := resultText(res); !strings.Contains(text, interruptHint) || !strings.Contains(text, "(Ctrl-C)") {
		t.Fatalf("bash text = %q, want the interrupt hint to name Ctrl-C alongside the %q escape", text, interruptHint)
	}

	interrupted := runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": interruptChar, "yield_time_ms": 2000})
	got := resultDetails(t, interrupted)
	if got["status"] != string(execsess.StatusExited) {
		t.Fatalf("after interrupt details = %+v, want exited", got)
	}
	if text := resultText(interrupted); !strings.Contains(text, "exited") {
		t.Fatalf("after interrupt text = %q, want exited status", text)
	}
}

func TestWriteStdinDrainsIncrementalOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	sessions := execsess.NewManager()
	bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
	poll := &WriteStdinTool{Sessions: sessions}

	res := runToolArgs(t, bash, map[string]any{"command": "sleep 0.3; echo later; sleep 30", "yield_time_ms": 100})
	id := resultDetails(t, res)["bash_id"].(string)

	res = runToolArgs(t, poll, map[string]any{"bash_id": id, "yield_time_ms": 3000})
	text := resultText(res)
	if !strings.Contains(text, "later") {
		t.Fatalf("poll text = %q, want the output produced after the first read", text)
	}
	if got := resultDetails(t, res)["status"]; got != string(execsess.StatusRunning) {
		t.Fatalf("status = %v, want running", got)
	}
	runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": interruptChar, "yield_time_ms": 2000})
}

func TestWriteStdinReportsExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	sessions := execsess.NewManager()
	bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
	poll := &WriteStdinTool{Sessions: sessions}

	res := runToolArgs(t, bash, map[string]any{"command": "sleep 0.3; exit 7", "yield_time_ms": 100})
	id := resultDetails(t, res)["bash_id"].(string)

	res = runToolArgs(t, poll, map[string]any{"bash_id": id, "yield_time_ms": 3000})
	details := resultDetails(t, res)
	if details["status"] != string(execsess.StatusExited) || details["exitCode"] != 7 {
		t.Fatalf("details = %+v, want exited with code 7", details)
	}
	if text := resultText(res); !strings.Contains(text, "exited code 7") {
		t.Fatalf("text = %q, want exited code 7", text)
	}
}

func TestWriteStdinUnknownID(t *testing.T) {
	sessions := execsess.NewManager()
	poll := &WriteStdinTool{Sessions: sessions}
	res := runToolArgs(t, poll, map[string]any{"bash_id": "bash_99"})
	if !strings.Contains(resultText(res), "no shell session") {
		t.Fatalf("text = %q, want unknown-session error", resultText(res))
	}
}

// The escaped spellings a model may emit for Ctrl-C must all interrupt, so the
// model does not need a retry round trip to discover the control character.
func TestWriteStdinEscapedInterruptSpellings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	for i, chars := range []string{`\u0003`, `^C`, "\ue002", `\uE002`, interruptChar + "\n"} {
		t.Run(fmt.Sprintf("spelling%d", i), func(t *testing.T) {
			sessions := execsess.NewManager()
			bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
			poll := &WriteStdinTool{Sessions: sessions}

			res := runToolArgs(t, bash, map[string]any{"command": "sleep 30", "yield_time_ms": 100})
			id := resultDetails(t, res)["bash_id"].(string)

			res = runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": chars, "yield_time_ms": 2000})
			details, ok := res.Details.(map[string]any)
			if !ok {
				t.Fatalf("chars %q: no details; text = %q", chars, resultText(res))
			}
			if got := details["status"]; got != string(execsess.StatusExited) {
				t.Fatalf("chars %q: status = %v, want exited", chars, got)
			}
		})
	}
}

// A pipe session rejects ordinary input (only Ctrl-C is meaningful there).
func TestWriteStdinPipeSessionRejectsInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	sessions := execsess.NewManager()
	bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
	poll := &WriteStdinTool{Sessions: sessions}

	res := runToolArgs(t, bash, map[string]any{"command": "sleep 30", "yield_time_ms": 100})
	id := resultDetails(t, res)["bash_id"].(string)
	res = runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": "y\n"})
	if !strings.Contains(resultText(res), "tty=true") {
		t.Fatalf("text = %q, want a tty=true hint", resultText(res))
	}
	runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": interruptChar, "yield_time_ms": 2000})
}

// A tty session round-trips a line of stdin through write_stdin.
func TestWriteStdinTTYInteractiveInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash not available on windows")
	}
	sessions := execsess.NewManager()
	bash := &BashTool{Dir: t.TempDir(), Sessions: sessions}
	poll := &WriteStdinTool{Sessions: sessions}

	res := runToolArgs(t, bash, map[string]any{"command": `read -r x; echo "got:$x"`, "yield_time_ms": 300, "tty": true})
	id := resultDetails(t, res)["bash_id"].(string)

	res = runToolArgs(t, poll, map[string]any{"bash_id": id, "chars": "hello\n", "yield_time_ms": 3000})
	if text := resultText(res); !strings.Contains(text, "got:hello") {
		t.Fatalf("text = %q, want got:hello", text)
	}
	if got := resultDetails(t, res)["status"]; got != string(execsess.StatusExited) {
		t.Fatalf("status = %v, want exited", got)
	}
}

func TestWriteStdinWithoutManager(t *testing.T) {
	poll := &WriteStdinTool{}
	res := runToolArgs(t, poll, map[string]any{"bash_id": "bash_1"})
	if !strings.Contains(resultText(res), "not available") {
		t.Fatalf("text = %q, want unavailable message", resultText(res))
	}
}
