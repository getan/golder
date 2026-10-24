package hostcompat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The workspace host is tested against a fake daemon on a real Unix socket,
// because the thing worth proving is the wire: that a pane of golder's own
// workspace is reported to directly, with no `tuios` binary in sight, and that
// the verbs and their fields are the ones the daemon documents.

// wsCall is one request the fake daemon received.
type wsCall struct {
	verb   string
	params map[string]any
}

// workspaceDaemon is a fake of golder's workspace control socket.
type workspaceDaemon struct {
	sock string

	mu     sync.Mutex
	calls  []wsCall
	conns  int
	answer func(verb string, params map[string]any) any
}

// wsError is an answer the daemon refused: the request was understood and
// declined, which is not a transport failure.
type wsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wsHangUp is an answer that closes the connection without replying, which is
// what a daemon restarting under a client looks like.
type wsHangUp struct{}

// startWorkspaceDaemon listens on a fake daemon socket. answer decides each
// reply; nil replies with {"applied": true}.
func startWorkspaceDaemon(t *testing.T, answer func(verb string, params map[string]any) any) *workspaceDaemon {
	t.Helper()
	// Keep the path short: macOS caps Unix socket paths at 104 bytes, and a
	// t.TempDir path is longer than that.
	sock := filepath.Join("/tmp", fmt.Sprintf("golder-workspace-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &workspaceDaemon{sock: sock, answer: answer}
	t.Cleanup(func() { _ = ln.Close(); _ = os.Remove(sock) })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.mu.Lock()
			d.conns++
			d.mu.Unlock()
			go d.serve(conn)
		}
	}()
	return d
}

func (d *workspaceDaemon) serve(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var req struct {
			ID     int            `json:"id"`
			Verb   string         `json:"verb"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			return
		}
		d.mu.Lock()
		d.calls = append(d.calls, wsCall{verb: req.Verb, params: req.Params})
		answer := d.answer
		d.mu.Unlock()

		var res any = map[string]any{"applied": true}
		if answer != nil {
			if r := answer(req.Verb, req.Params); r != nil {
				res = r
			}
		}
		if _, hangUp := res.(wsHangUp); hangUp {
			return
		}
		resp := map[string]any{"id": req.ID}
		if e, ok := res.(wsError); ok {
			resp["error"] = e
		} else {
			resp["result"] = res
		}
		b, _ := json.Marshal(resp)
		_, _ = conn.Write(append(b, '\n'))
	}
}

// callsMatching returns the recorded calls for one verb, in order.
func (d *workspaceDaemon) callsMatching(verb string) []wsCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []wsCall
	for _, c := range d.calls {
		if c.verb == verb {
			out = append(out, c)
		}
	}
	return out
}

// connections is how many times a client connected.
func (d *workspaceDaemon) connections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns
}

// waitForCall waits until the daemon has recorded a call of this verb, failing
// the test rather than sleeping a fixed time.
func (d *workspaceDaemon) waitForCall(t *testing.T, verb string) wsCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if calls := d.callsMatching(verb); len(calls) > 0 {
			return calls[len(calls)-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never received %s", verb)
		}
		time.Sleep(time.Millisecond)
	}
}

// workspaceFor opens a reporter for a fake workspace pane: the socket is the
// fake daemon's, PATH is empty and no host binary exists anywhere, which is the
// point — a workspace pane reports with nothing installed but golder.
func workspaceFor(t *testing.T, d *workspaceDaemon, rec *recorder) *Reporter {
	t.Helper()
	kv := workspacePane()
	kv["TUIOS_SOCKET"] = d.sock
	kv["PATH"] = ""
	looked := false
	rep := Open(Options{
		Getenv: env(kv),
		LookPath: func(string) (string, error) {
			looked = true
			return "", fmt.Errorf("no tuios binary on an empty PATH")
		},
		Run: rec.run,
	})
	if rep == nil {
		t.Fatal("Open returned nil inside a golder workspace pane")
	}
	if looked {
		t.Error("the workspace host looked for a tuios binary; it speaks the socket itself")
	}
	if got := rep.HostName(); got != MuxGolder {
		t.Fatalf("HostName = %q, want %q", got, MuxGolder)
	}
	return rep
}

// TestWorkspaceReportsOverItsOwnSocket pins the verbs and fields: the neutral
// states become the daemon's own, a blocked report says what it waits for, and
// the conversation rides the state report as it does for an external tuios.
func TestWorkspaceReportsOverItsOwnSocket(t *testing.T) {
	cases := []struct {
		name  string
		state State
		kind  Kind
		msg   string
		sess  SessionRef
		want  map[string]any
	}{
		{
			name:  "working",
			state: StateWorking,
			want:  map[string]any{"state": "working"},
		},
		{
			name:  "blocked becomes needs_input with its kind",
			state: StateBlocked,
			kind:  KindApproval,
			msg:   "approve Bash: rm -rf build",
			want:  map[string]any{"state": "needs_input", "kind": "approval", "message": "approve Bash: rm -rf build"},
		},
		{
			name:  "done carries the conversation",
			state: StateDone,
			sess:  SessionRef{ID: "20261011-231500-000123"},
			want:  map[string]any{"state": "done", "agent_session_id": "20261011-231500-000123"},
		},
		{
			name:  "errored",
			state: StateErrored,
			want:  map[string]any{"state": "errored"},
		},
		{
			name:  "idle",
			state: StateIdle,
			want:  map[string]any{"state": "idle"},
		},
		{
			// A cleared pane carries no conversation: the id belongs to a
			// conversation that is over.
			name:  "none clears the pane",
			state: StateNone,
			sess:  SessionRef{ID: "20261011-231500-000123"},
			want:  map[string]any{"state": "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := startWorkspaceDaemon(t, nil)
			rec := newRecorder()
			rep := workspaceFor(t, d, rec)
			defer rep.Close()

			if err := rep.host.Apply(t.Context(), Report{State: tc.state, Kind: tc.kind, Message: tc.msg}, tc.sess); err != nil {
				t.Fatalf("Apply = %v", err)
			}
			call := d.waitForCall(t, "set-agent-state")
			for k, want := range tc.want {
				if got := call.params[k]; got != want {
					t.Errorf("%s = %v, want %v (params %v)", k, got, want, call.params)
				}
			}
			// Every report names the pane and the harness, and says where it
			// came from, so the daemon ranks it above a screen reading.
			if call.params["window"] != "98db8226-1829-468e-89a8-41a2baa0ddab" {
				t.Errorf("window = %v, want the caller's own pane", call.params["window"])
			}
			if call.params["harness"] != Harness {
				t.Errorf("harness = %v, want %q", call.params["harness"], Harness)
			}
			if call.params["source"] != workspaceSource {
				t.Errorf("source = %v, want %q", call.params["source"], workspaceSource)
			}
			if call.params["session"] != "work" {
				t.Errorf("session = %v, want work", call.params["session"])
			}
			// A field that says nothing is left out, so a daemon that treats
			// "kind" or "message" as meaningful never sees an empty one.
			if _, ok := call.params["kind"]; ok && tc.kind == KindNone {
				t.Errorf("a report with no kind sent one: %v", call.params)
			}
			if _, ok := call.params["message"]; ok && tc.msg == "" {
				t.Errorf("a report with no message sent one: %v", call.params)
			}
			if _, ok := call.params["agent_session_id"]; ok && tc.want["agent_session_id"] == nil {
				t.Errorf("a report with no conversation sent one: %v", call.params)
			}
			// Nothing was exec'd: a workspace pane has no host binary to exec.
			if calls := rec.all(); len(calls) != 0 {
				t.Errorf("argv was run from a workspace pane: %v", calls)
			}
		})
	}
}

// TestWorkspaceSessionOnlyUsesTheSessionVerb: a conversation report must not
// move the pane's state, so it goes through the verb kept for exactly that.
func TestWorkspaceSessionOnlyUsesTheSessionVerb(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	rep := workspaceFor(t, d, newRecorder())
	defer rep.Close()

	if err := rep.host.Apply(t.Context(), Report{}, SessionRef{ID: "20261011-231500-000123"}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	call := d.waitForCall(t, "set-agent-session")
	if call.params["agent_session_id"] != "20261011-231500-000123" {
		t.Errorf("agent_session_id = %v", call.params["agent_session_id"])
	}
	if call.params["harness"] != Harness {
		t.Errorf("harness = %v, want %q", call.params["harness"], Harness)
	}
	if got := d.callsMatching("set-agent-state"); len(got) != 0 {
		t.Errorf("a conversation-only report moved the pane's state: %v", got)
	}
}

// TestWorkspaceMetaIsTokensWithATTL: metadata is the daemon's tokens object,
// with the lifetime, the source and the cut the external tuios host applies, so
// the rail reads the same whichever host is around.
func TestWorkspaceMetaIsTokensWithATTL(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	rep := workspaceFor(t, d, newRecorder())
	defer rep.Close()

	long := ""
	for range 200 {
		long += "中"
	}
	if err := rep.host.ApplyMeta(t.Context(), map[string]string{
		"model": "deepseek-v4.1-flash", "context": "42%", "summary": long, "bogus": "dropped",
	}); err != nil {
		t.Fatalf("ApplyMeta = %v", err)
	}
	call := d.waitForCall(t, "set-agent-meta")
	tokens, ok := call.params["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens = %T, want an object", call.params["tokens"])
	}
	if tokens["model"] != "deepseek-v4.1-flash" || tokens["context"] != "42%" {
		t.Errorf("tokens = %v", tokens)
	}
	if _, ok := tokens["bogus"]; ok {
		t.Errorf("an unknown metadata key was sent: %v", tokens)
	}
	summary, _ := tokens["summary"].(string)
	if n := len([]rune(summary)); n != tuiosMetaMaxValue {
		t.Errorf("summary has %d runes, want %d", n, tuiosMetaMaxValue)
	}
	if got := call.params["ttl_ms"]; got != float64(workspaceMetaTTL.Milliseconds()) {
		t.Errorf("ttl_ms = %v, want %d", got, workspaceMetaTTL.Milliseconds())
	}
	if call.params["source"] != Harness {
		t.Errorf("source = %v, want %q", call.params["source"], Harness)
	}
	if call.params["window"] != "98db8226-1829-468e-89a8-41a2baa0ddab" || call.params["session"] != "work" {
		t.Errorf("metadata went to the wrong pane: %v", call.params)
	}
}

// TestWorkspaceReleaseClearsTheState: golder going away leaves a shell behind,
// not an agent the rail still draws.
func TestWorkspaceReleaseClearsTheState(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	rep := workspaceFor(t, d, newRecorder())
	defer rep.Close()

	if err := rep.host.Release(t.Context()); err != nil {
		t.Fatalf("Release = %v", err)
	}
	call := d.waitForCall(t, "set-agent-state")
	if call.params["state"] != "none" {
		t.Errorf("release state = %v, want none", call.params["state"])
	}
	if call.params["harness"] != Harness {
		t.Errorf("harness = %v, want %q", call.params["harness"], Harness)
	}
}

// TestWorkspacePresentsItsPaneToken: the token proves which pane the connection
// is, so a report cannot land on a neighbour's window where the kernel cannot
// place the caller. It is presented once per connection, before the first
// report.
func TestWorkspacePresentsItsPaneToken(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	rep := workspaceFor(t, d, newRecorder())
	defer rep.Close()

	if err := rep.host.Apply(t.Context(), Report{State: StateWorking}, SessionRef{}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	d.waitForCall(t, "set-agent-state")
	d.mu.Lock()
	calls := append([]wsCall(nil), d.calls...)
	d.mu.Unlock()
	if len(calls) < 3 {
		t.Fatalf("calls = %v, want hello, pane-grants, set-agent-state", calls)
	}
	order := []string{calls[0].verb, calls[1].verb, calls[2].verb}
	if order[0] != "hello" || order[1] != "pane-grants" || order[2] != "set-agent-state" {
		t.Fatalf("call order = %v, want hello, pane-grants, set-agent-state", order)
	}
	if calls[1].params["pane_id"] != "98db8226-1829-468e-89a8-41a2baa0ddab" ||
		calls[1].params["pane_token"] != "proof-of-this-pane" {
		t.Errorf("pane-grants params = %v", calls[1].params)
	}
}

// TestWorkspaceWithoutATokenSkipsTheProof: a workspace may start panes with no
// token at all. The report still works, just without the placement proof.
func TestWorkspaceWithoutATokenSkipsTheProof(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	kv := workspacePane()
	kv["TUIOS_SOCKET"] = d.sock
	delete(kv, "TUIOS_PANE_TOKEN")
	rep := Open(Options{Getenv: env(kv), Run: newRecorder().run})
	if rep == nil {
		t.Fatal("Open returned nil for a workspace pane without a token")
	}
	defer rep.Close()

	if err := rep.host.Apply(t.Context(), Report{State: StateWorking}, SessionRef{}); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	d.waitForCall(t, "set-agent-state")
	if got := d.callsMatching("pane-grants"); len(got) != 0 {
		t.Errorf("pane-grants was sent without a token: %v", got)
	}
}

// TestWorkspaceReportThroughTheSession drives the reporter a driver holds: a
// session start is one idle report carrying the conversation, the release at
// Close clears the pane, and the socket is given back only after it.
func TestWorkspaceReportThroughTheSession(t *testing.T) {
	d := startWorkspaceDaemon(t, nil)
	rep := workspaceFor(t, d, newRecorder())

	const id = "20261011-231500-000123"
	rep.SessionState(id, ResumeArgv(id), StateIdle, KindNone, "")
	call := d.waitForCall(t, "set-agent-state")
	if call.params["state"] != "idle" || call.params["agent_session_id"] != id {
		t.Errorf("start params = %v", call.params)
	}

	rep.Release()
	rep.Close()
	released := d.callsMatching("set-agent-state")
	if len(released) < 2 || released[len(released)-1].params["state"] != "none" {
		t.Fatalf("the release did not reach the daemon: %v", released)
	}
}

// TestWorkspaceDaemonErrors proves a broken or refusing daemon is reported
// rather than swallowed, and that the report after a broken connection dials a
// fresh one with a fresh hello.
func TestWorkspaceDaemonErrors(t *testing.T) {
	t.Run("a refused verb is an error", func(t *testing.T) {
		d := startWorkspaceDaemon(t, func(verb string, _ map[string]any) any {
			if verb == "set-agent-state" {
				return wsError{Code: "unknown_verb", Message: "no such verb"}
			}
			return nil
		})
		rep := workspaceFor(t, d, newRecorder())
		defer rep.Close()

		err := rep.host.Apply(t.Context(), Report{State: StateWorking}, SessionRef{})
		if err == nil {
			t.Fatal("Apply to a daemon that refuses the verb = nil, want an error")
		}
		if !strings.Contains(err.Error(), "unknown_verb") {
			t.Errorf("error = %v, want the daemon's code in it", err)
		}
	})

	t.Run("a hung-up connection is an error, and the next report redials", func(t *testing.T) {
		var mu sync.Mutex
		hangUp := true
		d := startWorkspaceDaemon(t, func(verb string, _ map[string]any) any {
			mu.Lock()
			defer mu.Unlock()
			if verb == "set-agent-state" && hangUp {
				hangUp = false
				return wsHangUp{}
			}
			return nil
		})
		rep := workspaceFor(t, d, newRecorder())
		defer rep.Close()

		if err := rep.host.Apply(t.Context(), Report{State: StateWorking}, SessionRef{}); err == nil {
			t.Fatal("a report over a hung-up connection = nil, want an error")
		}
		if err := rep.host.Apply(t.Context(), Report{State: StateIdle}, SessionRef{}); err != nil {
			t.Fatalf("the report after a broken connection = %v", err)
		}
		if got := d.connections(); got < 2 {
			t.Errorf("connections = %d, want a redial", got)
		}
		if got := len(d.callsMatching("hello")); got < 2 {
			t.Errorf("hello count = %d, want a fresh handshake", got)
		}
	})

	t.Run("an unreachable daemon is an error the reporter logs", func(t *testing.T) {
		kv := workspacePane()
		kv["TUIOS_SOCKET"] = filepath.Join("/tmp", "golder-workspace-not-there.sock")
		var log syncBuffer
		rep := Open(Options{Getenv: env(kv), Run: newRecorder().run, ErrLog: &log, Timeout: 200 * time.Millisecond})
		if rep == nil {
			t.Fatal("Open returned nil for a pane whose daemon is down; it must open and drop the report")
		}
		rep.State(StateWorking, KindNone, "")
		rep.Close()
		if !strings.Contains(log.String(), "host report") {
			t.Errorf("log = %q, want one line about the failed report", log.String())
		}
	})
}

// syncBuffer is a bytes.Buffer safe for the reporter's own goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
