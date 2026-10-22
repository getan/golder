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
	"github.com/getan/golder/internal/tuiosctl"
)

type tuiosFake struct {
	mu     sync.Mutex
	verbs  []string
	params []map[string]any
	answer func(string, map[string]any) any
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
		result := f.result(req.Verb, req.Params)
		resp := map[string]any{"id": req.ID, "result": result}
		if e, ok := result.(*tuiosctl.Error); ok {
			resp["error"] = e
			delete(resp, "result")
		} else if e, ok := result.(map[string]any)["error"].(map[string]any); ok {
			resp["error"] = e
			delete(resp, "result")
		}
		b, _ := json.Marshal(resp)
		_, _ = conn.Write(append(b, '\n'))
	}
}

func (f *tuiosFake) result(verb string, params map[string]any) any {
	if f.answer != nil {
		return f.answer(verb, params)
	}
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
		if params["condition"] == "window-exit" {
			// The close watcher is only a race guard; the ordinary fake pane
			// does not close. Report the watcher's own timeout, which the tool
			// deliberately ignores.
			return map[string]any{"error": map[string]any{"code": "timeout", "message": "not closed"}}
		}
		return map[string]any{"type": "wait_result", "condition": "window-output", "matched": true, "pattern": "READY"}
	case "capture-pane":
		return map[string]any{"type": "pane_content", "source": "visible", "content": "READY\n", "revision": 7}
	case "get-window":
		return map[string]any{"type": "window", "window_id": "w1", "display_name": "golder"}
	case "move-window":
		return map[string]any{"type": "window_moved", "window_id": "w2", "from_workspace": 2, "workspace": 3, "current_workspace": 2}
	case "resize":
		return map[string]any{"type": "resized", "width": 100, "height": 40}
	case "set-window":
		return map[string]any{"type": "window_set", "window_id": "w2", "display_name": "api", "minimized": false}
	case "close-window", "focus-window":
		return map[string]any{"type": "ok"}
	}
	return map[string]any{"type": "ok"}
}

func useTuiosFake(t *testing.T, sock string) {
	t.Helper()
	t.Setenv("TUIOS_ENV", "1")
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

func TestTuiosRunFallsBackWithoutShellIntegration(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = func(verb string, params map[string]any) any {
		switch verb {
		case "hello":
			return map[string]any{"type": "hello"}
		case "run":
			return &tuiosctl.Error{Code: "no_shell_integration", Message: "shell has no OSC 133 marks"}
		case "wait-for":
			if params["condition"] == "window-exit" {
				return &tuiosctl.Error{Code: "timeout", Message: "not closed"}
			}
			return map[string]any{"type": "wait_result", "condition": "window-output", "matched": true}
		case "capture-pane":
			return map[string]any{"type": "pane_content", "source": "visible", "content": "FALLBACK OUTPUT"}
		case "send-text":
			return map[string]any{"type": "ok"}
		}
		return map[string]any{"type": "ok"}
	}
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"run","window":"w2","command":"echo hi"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("fallback run failed: %s", agentcore.ContentToText(res.Content))
	}
	if !f.seen("send-text") {
		t.Fatal("fallback did not type the command")
	}
	d, _ := res.Details.(map[string]any)
	if fallback, _ := d["fallback"].(bool); !fallback {
		t.Fatalf("details = %#v, want fallback", res.Details)
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

func TestTuiosMoveResizeRename(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	for _, raw := range []string{
		`{"action":"move","window":"w2","workspace":3,"follow":true}`,
		`{"action":"resize","window":"w2","width":100,"height":40}`,
		`{"action":"rename","window":"w2","name":"api"}`,
		`{"action":"minimize","window":"w2","minimized":true}`,
	} {
		res, err := tool.Execute(context.Background(), "1", json.RawMessage(raw), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("%s failed: %s", raw, agentcore.ContentToText(res.Content))
		}
	}
	if !f.seen("move-window") || !f.seen("resize") || !f.seen("set-window") {
		t.Fatalf("verbs = %v, want move-window, resize, set-window", f.verbs)
	}
}

func TestTuiosSendWritesText(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"send","window":"w2","text":"yes\n","submit":true,"paste":true}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("send failed: %s", agentcore.ContentToText(res.Content))
	}
	if !f.seen("send-text") {
		t.Fatal("send-text was not sent")
	}
}

func TestTuiosWaitStopsWhenPaneCloses(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = func(verb string, params map[string]any) any {
		switch verb {
		case "hello":
			return map[string]any{"type": "hello"}
		case "list-windows":
			return map[string]any{
				"type": "window_list", "total": 1, "focused_window_id": "w2",
				"windows": []map[string]any{{"window_id": "w2", "display_name": "build"}},
			}
		case "wait-for":
			if params["condition"] == "window-exit" {
				return map[string]any{"type": "wait_result", "condition": "window-exit", "matched": true}
			}
			time.Sleep(2 * time.Second)
			return map[string]any{"type": "wait_result", "condition": "window-output", "matched": true}
		}
		return map[string]any{"type": "ok"}
	}
	tool := newTuiosTool(t, true)
	start := time.Now()
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"wait","window":"w2","condition":"window-output","pattern":"READY","timeout_ms":10000}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "pane closed") {
		t.Fatalf("wait result = %s, want a pane-closed error", agentcore.ContentToText(res.Content))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("wait took %s after the pane closed", elapsed)
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

func TestTuiosCloseValidatesAgainstListNotGetWindow(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := newTuiosTool(t, true)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"close","window":"w2"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("close w2 failed: %s", agentcore.ContentToText(res.Content))
	}
	if f.seen("get-window") {
		t.Error("close must validate against list-windows, not the routed get-window response")
	}
	if !f.seen("close-window") {
		t.Fatal("close-window was not sent")
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
