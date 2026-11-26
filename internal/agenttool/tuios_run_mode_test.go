package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func fakePaneWindows(foreground string) func(string, map[string]any) any {
	return func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{
				"type": "window_list", "focused_window_id": "w2",
				"windows": []map[string]any{{"window_id": "w2", "display_name": "target", "foreground_cmd": foreground}},
			}
		}
		if verb == "run" {
			return map[string]any{"type": "command_result", "window": "w2", "output": "ok\n", "exit_code": 0}
		}
		return map[string]any{"type": "ok"}
	}
}

func TestTuiosRunRemotePaneDefaultsToScript(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = fakePaneWindows("ssh root@host")
	tool := newTuiosTool(t, true)
	_, _ = tool.Execute(agentcore.WithNetworkGrant(context.Background()), "1",
		json.RawMessage(`{"action":"run","window":"w2","command":"echo hi"}`), nil)
	if f.seen("run") {
		t.Fatal("a remote pane's inferred mode used the command path")
	}
	if !f.seen("send-text") || !f.seen("wait-for") {
		t.Fatalf("the inferred script path did not start: verbs=%v", f.verbs)
	}
}

func TestTuiosRunRemotePaneRejectsExplicitCommandBeforeWrite(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = fakePaneWindows("ssh root@host")
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(agentcore.WithNetworkGrant(context.Background()), "1",
		json.RawMessage(`{"action":"run","window":"w2","mode":"command","command":"echo hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := agentcore.ContentToText(res.Content)
	if !res.IsError || !strings.Contains(text, "remote shell detected") || !strings.Contains(text, "use run with script") {
		t.Fatalf("explicit command on a remote pane = %s", text)
	}
	if f.seen("send-text") || f.seen("run") {
		t.Fatalf("rejected call wrote to the pane: verbs=%v", f.verbs)
	}
}

func TestTuiosRunLocalPaneDefaultsToCommand(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = fakePaneWindows("zsh")
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"run","window":"w2","command":"echo hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !f.seen("run") {
		t.Fatalf("local inferred command path failed: %s verbs=%v", agentcore.ContentToText(res.Content), f.verbs)
	}
}

func TestTuiosRunUnknownSkipsRetryAndSplitsHint(t *testing.T) {
	remote := tuiosRunUnknown("w2", errors.New("not_at_prompt"), true)
	local := tuiosRunUnknown("w2", errors.New("not_at_prompt"), false)
	for _, result := range []agentcore.AgentToolResult{remote, local} {
		details := result.Details.(map[string]any)
		if !result.IsError || details["execution_state"] != "unknown" || details["retryable"] != false {
			t.Fatalf("unknown result = %+v", details)
		}
	}
	remoteText := agentcore.ContentToText(remote.Content)
	localText := agentcore.ContentToText(local.Content)
	if !strings.Contains(remoteText, "remote (SSH/mosh) pane; use run with script") {
		t.Fatalf("remote hint = %s", remoteText)
	}
	if !strings.Contains(localText, "local shell may be blocked") || strings.Contains(localText, "remote (SSH/mosh)") {
		t.Fatalf("local hint = %s", localText)
	}
}
