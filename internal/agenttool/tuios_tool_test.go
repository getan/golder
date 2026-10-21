package agenttool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

type tuiosFake struct {
	mu     sync.Mutex
	verbs  []string
	params []map[string]any
}

func (f *tuiosFake) seen(verb string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.verbs {
		if v == verb {
			return true
		}
	}
	return false
}

func startTuiosFake(t *testing.T) (*tuiosFake, string) {
	t.Helper()
	f := &tuiosFake{}
	sock := filepath.Join("/tmp", fmt.Sprintf("golder-tuios-tool-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = os.Remove(sock) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f, sock
}

func (f *tuiosFake) serve(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var req struct {
			ID     int            `json:"id"`
			Verb   string         `json:"verb"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return
		}
		f.mu.Lock()
		f.verbs = append(f.verbs, req.Verb)
		f.params = append(f.params, req.Params)
		f.mu.Unlock()
		result := f.result(req.Verb)
		resp := map[string]any{"id": req.ID, "result": result}
		if e, ok := result.(map[string]any)["error"].(map[string]any); ok {
			resp["error"] = e
			delete(resp, "result")
		}
		b, _ := json.Marshal(resp)
		_, _ = conn.Write(append(b, '\n'))
	}
}

func (f *tuiosFake) result(verb string) any {
	switch verb {
	case "hello":
		return map[string]any{"type": "hello"}
	case "list-windows":
		return map[string]any{
			"type": "window_list", "total": 2, "focused_window_id": "w1",
			"windows": []map[string]any{
				{"window_id": "w1", "display_name": "golder", "focused": true},
				{"window_id": "w2", "display_name": "build", "focused": false, "workspace": 2},
			},
		}
	case "run":
		return map[string]any{"type": "command_result", "window": "w1", "output": "hello\n", "exit_code": 0}
	case "send-text":
		return map[string]any{"type": "ok"}
	case "wait-for":
		return map[string]any{"type": "wait_result", "condition": "window-output", "matched": true, "pattern": "READY"}
	case "capture-pane":
		return map[string]any{"type": "pane_content", "source": "visible", "content": "READY\n", "revision": 7}
	case "get-window":
		return map[string]any{"type": "window", "window_id": "w1", "display_name": "golder"}
	case "close-window", "focus-window":
		return map[string]any{"type": "ok"}
	}
	return map[string]any{"type": "ok"}
}

func useTuiosFake(t *testing.T, sock string) {
	t.Helper()
	t.Setenv("TUIOS_SOCKET", sock)
	t.Setenv("TUIOS_SESSION", "work")
	t.Setenv("TUIOS_PANE_ID", "w1")
}

func newTuiosTool(t *testing.T, allowControl bool) agentcore.AgentTool {
	t.Helper()
	return NewTuiosTool(t.TempDir(), allowControl)
}

func TestTuiosRunUsesEventWaitForExpect(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"run","window":"w2","command":"make","expect":"WANT"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("run failed: %s", agentcore.ContentToText(res.Content))
	}
	if !f.seen("send-text") || !f.seen("wait-for") || !f.seen("capture-pane") {
		t.Fatalf("verbs = %v, want send-text, wait-for, capture-pane", f.verbs)
	}
	if f.seen("run") {
		t.Error("expect path must use wait-for, not the finished-only run verb")
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "READY") || !strings.Contains(text, "matched") {
		t.Fatalf("result = %s", text)
	}
}

func TestTuiosListCoversExistingPanes(t *testing.T) {
	_, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"list"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, "w2") || !strings.Contains(text, "build") {
		t.Fatalf("list = %s, want the existing non-golder pane", text)
	}
}

func TestTuiosWindowRefusesOwnPane(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"close","window":"w1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "refusing") {
		t.Fatalf("close own pane = %s, want refusal", agentcore.ContentToText(res.Content))
	}
	if f.seen("close-window") {
		t.Error("close-window must not be sent for golder's own pane")
	}
}

func TestTuiosChildFacadeIsReadOnly(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, false)
	if strings.Contains(string(tool.Schema()), `"run"`) {
		t.Fatalf("child schema advertises control actions: %s", tool.Schema())
	}
	res, _ := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"run","command":"echo hi"}`), nil)
	if !res.IsError {
		t.Fatalf("child control action = %s, want refusal", agentcore.ContentToText(res.Content))
	}
	if f.seen("run") {
		t.Error("child control action reached the daemon")
	}
}
