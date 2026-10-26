package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/permissions"
)

func TestBashToolSignalExitReportsReason(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	for _, tc := range []struct{ signal, reason string }{
		{"KILL", "signal: killed"}, {"TERM", "signal: terminated"},
	} {
		t.Run(tc.signal, func(t *testing.T) {
			sessions := execsess.NewManager()
			t.Cleanup(sessions.KillAll)
			tool := &BashTool{Sessions: sessions, Permissions: permissions.New(permissions.FullAccess)}
			res, err := runBash(t, tool, map[string]any{"command": "printf before-kill; kill -" + tc.signal + " $$"}, nil)
			if err == nil || !strings.HasPrefix(err.Error(), "bash: command terminated: "+tc.reason+"\n") {
				t.Fatalf("error = %v, want the actual signal termination reason", err)
			}
			if !strings.Contains(err.Error(), "before-kill") || !strings.Contains(resultText(res), "before-kill") {
				t.Fatal("signal failure discarded captured command output")
			}
			details := resultDetails(t, res)
			if details["exitCode"] != -1 || details["processError"] != tc.reason || details["envelope"] != "bare" {
				t.Fatalf("signal failure metadata = %#v", details)
			}
		})
	}
}

func TestBashSignalExitSurvivesExecutor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	sessions := execsess.NewManager()
	t.Cleanup(sessions.KillAll)
	tool := &BashTool{Sessions: sessions, Permissions: permissions.New(permissions.FullAccess)}
	args := json.RawMessage(`{"command":"kill -KILL $$"}`)
	res, err, isError := runTool(context.Background(), tool, agentcore.AgentToolCall{ID: "signal-call", Name: "bash"}, args, nil)
	if err == nil || !isError || !strings.HasPrefix(resultText(res), `tool "bash" failed: bash: command terminated: signal: killed`) {
		t.Fatalf("executor dropped the real failure: isError=%t err=%v text=%q", isError, err, resultText(res))
	}
	if details := resultDetails(t, res); details["exitCode"] != -1 || details["processError"] != "signal: killed" {
		t.Fatalf("executor discarded process metadata: %#v", details)
	}
}

func TestBashSignalExitMatchesBackgroundSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	sessions := execsess.NewManager()
	t.Cleanup(sessions.KillAll)
	dir := t.TempDir()
	bash := &BashTool{Dir: dir, Sessions: sessions, Permissions: permissions.New(permissions.FullAccess)}
	front, frontErr := runBash(t, bash, map[string]any{"command": "kill -KILL $$"}, nil)
	if frontErr == nil {
		t.Fatal("expected a foreground signal error")
	}
	reason, _ := resultDetails(t, front)["processError"].(string)
	if reason == "" {
		t.Fatal("foreground processError missing")
	}
	// Hold the child at an explicit gate: it cannot race the initial yield and
	// accidentally exercise the foreground branch instead of write_stdin.
	back := runToolArgs(t, bash, map[string]any{
		"command": "until [ -f release ]; do sleep 0.01; done; kill -KILL $$", "yield_time_ms": 100,
	})
	id, _ := resultDetails(t, back)["bash_id"].(string)
	if id == "" {
		t.Fatal("gated command did not yield a background session")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sess, ok := sessions.Get(id)
	if !ok || !sess.Wait(context.Background(), 5*time.Second, execsess.WaitExit) {
		t.Fatal("background session did not finish")
	}
	back = runToolArgs(t, &WriteStdinTool{Sessions: sessions}, map[string]any{"bash_id": id})
	if got := resultDetails(t, back); got["exitCode"] != -1 || got["processError"] != reason {
		t.Fatalf("foreground/background reasons disagree: front=%q back=%#v", reason, got)
	}
	if !strings.Contains(frontErr.Error(), reason) || !strings.Contains(resultText(back), ": exited: "+reason+"]") {
		t.Fatalf("foreground/background text disagrees: front=%v back=%q", frontErr, resultText(back))
	}
}
