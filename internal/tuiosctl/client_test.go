package tuiosctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDaemon starts a Unix socket that answers one JSON line per request.
func fakeDaemon(t *testing.T, answer func(verb string, params map[string]any) any) string {
	t.Helper()
	return fakeDaemonCounting(t, nil, answer)
}

// hangUpAnswer is the answer that hangs up instead of replying, which is what a
// daemon restarting under a client looks like.
type hangUpAnswer struct{}

var fakeHangUp hangUpAnswer

// fakeDaemonCounting is fakeDaemon that also counts the connections it
// accepted, so a test can tell a reused connection from a fresh dial.
func fakeDaemonCounting(t *testing.T, conns *int, answer func(verb string, params map[string]any) any) string {
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
			if conns != nil {
				*conns = *conns + 1
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
		if _, hangUp := res.(hangUpAnswer); hangUp {
			return
		}
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

func TestClientNewWindowRetriesWithoutCloseOnExitOnOldDaemon(t *testing.T) {
	var calls []map[string]any
	sock := fakeDaemon(t, func(verb string, params map[string]any) any {
		switch verb {
		case "hello":
			return map[string]any{"type": "hello"}
		case "new-window":
			calls = append(calls, params)
			if _, ok := params["close_on_exit"]; ok {
				return &Error{Code: "invalid_params", Message: "verb new-window has no parameter close_on_exit"}
			}
			return map[string]any{"type": "window_created", "window_id": "w1"}
		default:
			return map[string]any{"type": "ok"}
		}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.NewWindow(context.Background(), NewWindowParams{
		Name: "test", Focus: false, CloseOnExit: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("new-window calls = %d, want 2 (retry)", len(calls))
	}
	if _, ok := calls[1]["close_on_exit"]; ok {
		t.Fatalf("retry still sent close_on_exit: %v", calls[1])
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

// TestReadLineStopsAtTheLimit is the fix for the unbounded read: the size was
// checked on ReadBytes' result, which is after bufio grew its buffer to fit
// whatever arrived. A peer that never sends a newline must cost at most the
// limit, and a line that ends exactly at the limit is still a line.
func TestReadLineStopsAtTheLimit(t *testing.T) {
	const limit = 64

	t.Run("exactly at the limit", func(t *testing.T) {
		line := append(bytes.Repeat([]byte("x"), limit-1), '\n')
		got, err := readLine(bufio.NewReaderSize(bytes.NewReader(line), 8), limit)
		if err != nil {
			t.Fatalf("readLine = %v, want the line", err)
		}
		if !bytes.Equal(got, line) {
			t.Fatalf("readLine = %q, want %q", got, line)
		}
	})

	t.Run("one byte past the limit", func(t *testing.T) {
		line := append(bytes.Repeat([]byte("x"), limit), '\n')
		if _, err := readLine(bufio.NewReaderSize(bytes.NewReader(line), 8), limit); !errors.Is(err, errLineTooLong) {
			t.Fatalf("readLine = %v, want errLineTooLong", err)
		}
	})

	t.Run("no newline ever arrives", func(t *testing.T) {
		// Four times the limit, in fragments smaller than the reader's buffer,
		// so the refusal comes from the limit and not from a single read.
		endless := bytes.NewReader(bytes.Repeat([]byte("x"), limit*4))
		if _, err := readLine(bufio.NewReaderSize(endless, 8), limit); !errors.Is(err, errLineTooLong) {
			t.Fatalf("readLine = %v, want errLineTooLong", err)
		}
		// The reader is left where the refusal happened, so the rest of the
		// wire is still readable; the client drops the connection rather than
		// resync, which this only pins as "not silently consumed".
		if endless.Len() == 0 {
			t.Error("readLine consumed the whole stream before refusing")
		}
	})

	t.Run("a line longer than the buffer", func(t *testing.T) {
		body := bytes.Repeat([]byte("y"), limit/2)
		got, err := readLine(bufio.NewReaderSize(bytes.NewReader(append(body, '\n')), 8), limit)
		if err != nil {
			t.Fatalf("readLine = %v, want the line assembled from fragments", err)
		}
		if !bytes.Equal(got, append(body, '\n')) {
			t.Fatalf("readLine = %q, want %q", got, append(body, '\n'))
		}
	})

	t.Run("end of stream mid-line", func(t *testing.T) {
		// Half a response parsed as a whole one would be worse than none.
		if _, err := readLine(bufio.NewReaderSize(bytes.NewReader([]byte(`{"id":1`)), 8), limit); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("readLine = %v, want io.ErrUnexpectedEOF", err)
		}
	})
}

// shortConn is a net.Conn whose Write takes at most max bytes per call, which
// is what a socket is allowed to do. Only Write is reached by writeAll.
type shortConn struct {
	net.Conn
	max int
	buf bytes.Buffer
}

func (c *shortConn) Write(p []byte) (int, error) {
	if len(p) > c.max {
		p = p[:c.max]
	}
	return c.buf.Write(p)
}

// TestWriteAllCompletesAShortWrite: net.Conn may accept part of a buffer, and
// a caller that trusted the first nil error would send a truncated command.
func TestWriteAllCompletesAShortWrite(t *testing.T) {
	conn := &shortConn{max: 3}
	want := []byte(`{"id":1,"verb":"list-windows"}` + "\n")
	if err := writeAll(conn, want); err != nil {
		t.Fatalf("writeAll = %v, want nil", err)
	}
	if got := conn.buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("wrote %q, want %q", got, want)
	}

	// A connection that accepts nothing must be an error, never a silent
	// success with the request lost.
	if err := writeAll(&shortConn{max: 0}, want); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll over a stuck conn = %v, want io.ErrShortWrite", err)
	}
}

// TestClientTimeoutIsADeadline: the caller distinguishes "the daemon was slow"
// from "the connection broke", so a timeout has to be testable as a deadline
// while its message stays readable.
func TestClientTimeoutIsADeadline(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	sock := fakeDaemon(t, func(verb string, _ map[string]any) any {
		if verb == "hello" {
			return map[string]any{"type": "hello"}
		}
		<-block
		return map[string]any{"type": "ok"}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.ListWindows(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListWindows = %v, want a deadline error", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not say it timed out", err)
	}
	// The connection that timed out is dropped, so the next call dials a fresh
	// one instead of reading the late reply as its own answer.
	if !strings.Contains(err.Error(), "list-windows") {
		t.Errorf("error %q does not name the verb", err)
	}
}

// TestClientReconnectsAfterTheConnectionBreaks: a daemon restart (or a socket
// that vanished) fails the call in flight but must not poison the client.
func TestClientReconnectsAfterTheConnectionBreaks(t *testing.T) {
	var conns int
	sock := fakeDaemonCounting(t, &conns, func(verb string, _ map[string]any) any {
		switch verb {
		case "hello":
			return map[string]any{"type": "hello"}
		case "close-window":
			// Hang up mid-call: the client sees EOF and drops the connection.
			return fakeHangUp
		default:
			return map[string]any{"type": "window_list", "total": 0}
		}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.CloseWindow(context.Background(), "w1"); err == nil {
		t.Fatal("a hung-up call should fail")
	}
	if _, err := c.ListWindows(context.Background()); err != nil {
		t.Fatalf("the call after a broken connection = %v, want a fresh dial", err)
	}
	if conns < 2 {
		t.Errorf("connections = %d, want the second call to redial", conns)
	}
}

// TestClientCancelledContextDoesNotDial: a cancelled call must not reach the
// daemon, and must report the cancellation rather than a connection error.
func TestClientCancelledContextDoesNotDial(t *testing.T) {
	sock := fakeDaemon(t, func(verb string, _ map[string]any) any {
		return map[string]any{"type": "hello"}
	})
	c, err := Dial(context.Background(), Options{Socket: sock, Session: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListWindows(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListWindows on a cancelled context = %v, want context.Canceled", err)
	}
	// The connection was never used, so it is still good.
	if _, err := c.ListWindows(context.Background()); err != nil {
		t.Fatalf("the next call = %v, want the existing connection", err)
	}
}
