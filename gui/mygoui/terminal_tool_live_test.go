package mygoui

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

// This opt-in test uses a caller-provided, already installed libghostty; it
// never downloads a native library or retries a denied PTY allocation.
func TestTerminalToolLiveAgentPTY(t *testing.T) {
	if os.Getenv("GOLDER_GUI_TERMINAL_LIVE_TEST") != "1" || os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("set GOLDER_GUI_TERMINAL_LIVE_TEST=1 and MYGO_GHOSTTY_VT to an existing native library")
	}
	tool, parent, _ := terminalToolFixture(t)
	tool.app.terminalProject(parent.scope()).root = nil
	tool.app.terminalRegistry().RemovePane(parent.id)
	tool.guard.Mode.Set(permissions.FullAccess)
	reg := agenttool.NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	gate := judge.ConsentGate(judge.ConsentOpts{Mode: tool.guard.Mode, Cwd: tool.agent.project})
	execute := func(id string, args any) agentcore.ToolResultMessage {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		results, _ := agenttool.ExecuteToolCalls(context.Background(), agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
			Registry: reg, BeforeToolCall: gate,
		}}, []agentcore.AgentToolCall{{ID: id, Name: "terminal", Arguments: raw}}, nil)
		if id == "new" && len(results) == 1 && results[0].IsError {
			message := agentcore.ContentToText(results[0].Content)
			if strings.Contains(message, "allocate PTY") && (strings.Contains(message, "operation not permitted") || strings.Contains(message, "permission denied")) {
				t.Skipf("PTY allocation denied by the sandbox: %s", message)
			}
		}
		if len(results) != 1 || results[0].IsError {
			t.Fatalf("%s: %+v", id, results)
		}
		return results[0]
	}
	created := execute("new", map[string]any{"action": "new"})
	var result struct {
		Pane string `json:"pane"`
	}
	if err := json.Unmarshal([]byte(agentcore.ContentToText(created.Content)), &result); err != nil || result.Pane == "" {
		t.Fatalf("new terminal result=%q err=%v", agentcore.ContentToText(created.Content), err)
	}
	pane := tool.app.findTerminalPane(tool.agent.scope(), result.Pane)
	if pane == nil || pane.term == nil || pane.conn == nil || !pane.conn.ViewLocked() {
		t.Fatalf("created terminal is not attached and locked: %+v", pane)
	}
	t.Cleanup(func() { tool.app.closeTerminalPane(pane) })
	execute("send", map[string]any{"action": "send", "pane": result.Pane, "text": "printf '__GOLDER_GUI_TERMINAL_OK__\\n'; exit\r"})
	select {
	case <-pane.term.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Agent shell did not exit")
	}
	captured := execute("capture", map[string]any{"action": "capture", "pane": result.Pane, "lines": 80})
	if text := agentcore.ContentToText(captured.Content); !strings.Contains(text, "__GOLDER_GUI_TERMINAL_OK__") {
		t.Fatalf("final output was not retained: %s", text)
	}
	execute("close", map[string]any{"action": "close", "pane": result.Pane})
	if tool.app.findTerminalPane(tool.agent.scope(), result.Pane) != nil {
		t.Fatal("closed Agent pane remained in its project")
	}
}
