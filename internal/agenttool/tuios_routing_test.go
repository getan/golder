package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func TestTuiosInputParallelAcrossPanes(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var count atomic.Int32
	f.answer = func(verb string, p map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"type": "window_list", "windows": []map[string]any{{"window_id": "w1"}, {"window_id": "w2", "display_name": "server-a"}, {"window_id": "w3", "display_name": "server-b"}}}
		}
		if verb == "run" {
			if p["window"] == "w2" && count.Add(1) == 1 {
				close(started)
				<-release
			}
			return map[string]any{"type": "command_result", "window": p["window"], "output": "ok", "exit_code": 0}
		}
		return map[string]any{"type": "ok"}
	}
	tool := newTuiosTool(t, true)
	doneA, doneB := make(chan agentcore.AgentToolResult, 1), make(chan agentcore.AgentToolResult, 1)
	go func() {
		r, _ := tool.Execute(context.Background(), "request-a", json.RawMessage(`{"action":"run","window":"w2","command":"slow"}`), nil)
		doneA <- r
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pane did not start")
	}
	go func() {
		r, _ := tool.Execute(context.Background(), "request-b", json.RawMessage(`{"action":"run","window":"w3","command":"fast"}`), nil)
		doneB <- r
	}()
	select {
	case r := <-doneB:
		text := agentcore.ContentToText(r.Content)
		if r.IsError || !strings.Contains(text, "request-b") || !strings.Contains(text, "server-b") || !strings.Contains(text, "w3") {
			t.Fatalf("bad routing: %s", text)
		}
	case <-time.After(time.Second):
		t.Fatal("a slow pane blocked another server")
	}
	once.Do(func() { close(release) })
	if r := <-doneA; r.IsError {
		t.Fatal(agentcore.ContentToText(r.Content))
	}
}

func TestTuiosInputClaimCancellation(t *testing.T) {
	tool := NewTuiosTool(t.TempDir(), true)
	release, err := tool.claimInput(context.Background(), "same-pane")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = tool.claimInput(ctx, "same-pane"); err == nil {
		t.Fatal("cancelled waiter acquired busy pane")
	}
	release()
	if len(tool.inputs) != 0 {
		t.Fatal("input claim leaked")
	}
}

func TestTuiosRemoteScriptCannotFallBackToLocalShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist")
	p, err := newTuiosScript("touch "+quoteControlArg(path), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.bootstrap) > 3072 {
		t.Fatal("remote setup exceeds terminal line budget")
	}
	cmd := exec.Command("sh", "-c", p.receiver)
	cmd.Env = append(os.Environ(), "SSH_CONNECTION=")
	cmd.Stdin = strings.NewReader(p.frames)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("local receiver accepted remote script: %s", out)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("remote script executed locally")
	}
}
