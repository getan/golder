package tuiosctl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeDaemon starts a Unix socket that answers one JSON line per request.
func fakeDaemon(t *testing.T, answer func(verb string, params map[string]any) any) string {
	t.Helper()
	// Keep the path short: macOS caps Unix socket paths at 104 bytes, and a
	// t.TempDir path is longer than that.
	sock := filepath.Join("/tmp", fmt.Sprintf("golder-tuios-%d.sock", time.Now().UnixNano()))
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
			go serveFake(t, conn, answer)
		}
	}()
	return sock
}

func serveFake(t *testing.T, conn net.Conn, answer func(string, map[string]any) any) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), maxLineBytes)
	for sc.Scan() {
		var req struct {
			ID     int            `json:"id"`
			Verb   string         `json:"verb"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return
		}
		res := answer(req.Verb, req.Params)
		resp := map[string]any{"id": req.ID}
		if e, ok := res.(*Error); ok {
			resp["error"] = e
		} else {
			resp["result"] = res
		}
		b, _ := json.Marshal(resp)
		_, _ = conn.Write(append(b, '\n'))
	}
}

func TestClientListWindowsAndRequestIDs(t *testing.T) {
	var verbs []string
	sock := fakeDaemon(t, func(verb string, params map[string]any) any {
		verbs = append(verbs, verb)
		if verb == "hello" {
			return map[string]any{"type": "hello", "protocol": 1}
		}
		if params["session"] != "work" {
			t.Errorf("session = %v, want work", params["session"])
		}
		return map[string]any{
			"type": "window_list", "total": 1, "focused_window_id": "w1",
			"windows": []map[string]any{{"window_id": "w1", "display_name": "golder", "focused": true}},
		}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	wl, err := c.ListWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wl.FocusedWindowID != "w1" || len(wl.Windows) != 1 {
		t.Fatalf("list = %+v", wl)
	}
	if len(verbs) != 2 || verbs[0] != "hello" || verbs[1] != "list-windows" {
		t.Fatalf("verbs = %v", verbs)
	}
}

func TestClientWaitTimeout(t *testing.T) {
	sock := fakeDaemon(t, func(verb string, _ map[string]any) any {
		if verb == "hello" {
			return map[string]any{"type": "hello"}
		}
		return &Error{Code: "timeout", Message: "no output"}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.WaitFor(context.Background(), WaitParams{
		Condition: "window-output", Window: "w1", Pattern: "PASS", Timeout: 100 * time.Millisecond,
	})
	var de *Error
	if !errors.As(err, &de) || de.Code != "timeout" {
		t.Fatalf("wait err = %v, want timeout", err)
	}
}

// close_on_exit is newer than some daemons. Sending false unconditionally made
// an older daemon reject every new-window call; the client must omit the field
// unless it is true. The fake daemon records the actual params, which the
// higher-level tool tests deliberately do not validate.
func TestClientNewWindowOnlySendsCloseOnExitWhenTrue(t *testing.T) {
	for _, want := range []bool{false, true} {
		got := make(chan map[string]any, 1)
		sock := fakeDaemon(t, func(verb string, params map[string]any) any {
			switch verb {
			case "hello":
				return map[string]any{"type": "hello"}
			case "new-window":
				got <- params
				return map[string]any{"type": "window_created", "window_id": "w1"}
			default:
				return map[string]any{"type": "ok"}
			}
		})
		c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.NewWindow(context.Background(), NewWindowParams{
			Name: "test", Focus: false, CloseOnExit: want,
		}); err != nil {
			t.Fatal(err)
		}
		params := <-got
		_, present := params["close_on_exit"]
		if present != want {
			t.Errorf("CloseOnExit=%v: close_on_exit present=%v, want %v (params=%v)",
				want, present, want, params)
		}
		_ = c.Close()
	}
}

func TestClientContextCancellationInterruptsABlockedRead(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	sock := fakeDaemon(t, func(verb string, _ map[string]any) any {
		if verb == "hello" {
			return map[string]any{"type": "hello"}
		}
		<-release
		return map[string]any{"type": "ok"}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ListWindows(ctx)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked call returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt the blocked read")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvEnv, "1")
	t.Setenv(EnvSocket, "/tmp/t.sock")
	t.Setenv(EnvSession, "work")
	t.Setenv(EnvPaneID, "w1")
	t.Setenv(EnvPaneToken, "proof")
	o := FromEnv()
	if o.Socket != "/tmp/t.sock" || o.Session != "work" || o.PaneID != "w1" || o.Proof != "proof" {
		t.Fatalf("opts = %+v", o)
	}
}

func TestAvailableRequiresARealPane(t *testing.T) {
	t.Setenv(EnvEnv, "1")
	t.Setenv(EnvSocket, "/tmp/t.sock")
	t.Setenv(EnvSession, "work")
	t.Setenv(EnvPaneID, "w1")
	if !Available() {
		t.Fatal("a complete pane environment must be available")
	}
	t.Setenv(EnvPaneID, "")
	if Available() {
		t.Fatal("a missing pane id must not be enough")
	}
	t.Setenv(EnvPaneID, "w1")
	t.Setenv(EnvEnv, "")
	if Available() {
		t.Fatal("a stale socket/session outside a tuios pane must not be enough")
	}
}
