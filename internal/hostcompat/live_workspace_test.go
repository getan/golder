//go:build darwin || linux

package hostcompat

// The workspace host against the real thing.
//
// Everything else in this package's tests pins the wire against a fake daemon,
// which is the right shape for the verbs. What a fake cannot answer is whether
// the daemon agrees: whether a connection that presents its pane's token is
// held to that pane and no other, whether a wrong token gets nothing at all,
// whether a read-only restriction refuses the verbs it should, and whether a
// nested golder — a sub-agent handed the pane's identity variables — reports
// about the pane it names rather than about a neighbour's.
//
// So these tests run the daemon golder embeds (package mini), let it start real
// panes with real tokens, and read the answer back over the same control
// socket the tools use. The test process itself runs in no pane of that daemon,
// exactly like a same-UID process that is not the person: it is the attacker in
// the tests that must be refused.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini"
	"github.com/getan/tuios-mini/mini/control"

	"github.com/getan/golder/internal/tuiosctl"
)

// liveHelperEnv re-executes the test binary as the nested golder of
// TestLiveWorkspaceNestedRunKeepsThePane. The value is the file to write when
// the report has been sent.
const liveHelperEnv = "GOLDER_HOSTCOMPAT_LIVE_HELPER"

// liveStopEnv names the file the helper waits for before it releases the pane.
const liveStopEnv = "GOLDER_HOSTCOMPAT_LIVE_STOP"

// TestMain is the helper's entry point as well as the package's. A child
// started with liveHelperEnv set is not a test run: it is the nested golder,
// and it reports for the pane its environment names.
func TestMain(m *testing.M) {
	if out := os.Getenv(liveHelperEnv); out != "" {
		os.Exit(runNestedGold(out))
	}
	os.Exit(m.Run())
}

// runNestedGold is the nested golder: it reports through the same Session a
// driver holds, then waits for the parent to say it has read the daemon's
// answer, then releases the pane. It returns the exit code.
func runNestedGold(out string) int {
	sess := OpenSession(Options{ErrLog: os.Stderr})
	if sess == nil {
		_ = os.WriteFile(out, []byte("no host: "+DescribePane(os.Environ())+"\n"), 0o600)
		return 1
	}
	// A conversation of its own, which is what makes this a nested run rather
	// than a repeat of the pane's agent.
	sess.Start("20261011-231500-000999", nil)
	sess.TurnStart()
	if err := os.WriteFile(out, []byte("reported\n"), 0o600); err != nil {
		return 1
	}
	stop := os.Getenv(liveStopEnv)
	deadline := time.Now().Add(20 * time.Second)
	for stop != "" && time.Now().Before(deadline) {
		if _, err := os.Stat(stop); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess.Close()
	return 0
}

// liveWorkspace is a real daemon with one real pane, and the identity that pane
// was started with.
type liveWorkspace struct {
	dir     string
	socket  string
	session string
	pane    string
	token   string
}

// tempDir is a private directory for a daemon socket: the platform's temp root
// first, /tmp second, and a skip naming the limit rather than a mystery failure
// when neither is short enough for sockaddr_un.
func tempDir(t *testing.T, prefix string) string {
	t.Helper()
	const socketRoom = 100
	try := func(root string) (string, error) {
		if root == "" {
			return os.MkdirTemp("", prefix)
		}
		return os.MkdirTemp(root, prefix)
	}
	first, firstErr := try("")
	if firstErr == nil && len(filepath.Join(first, "d.sock")) <= socketRoom {
		t.Cleanup(func() { _ = os.RemoveAll(first) })
		return first
	}
	if first != "" {
		_ = os.RemoveAll(first)
	}
	second, secondErr := try("/tmp")
	if secondErr != nil {
		t.Fatalf("no temp directory for a daemon socket (temp root: %v; /tmp: %v)", firstErr, secondErr)
	}
	if len(filepath.Join(second, "d.sock")) > socketRoom {
		_ = os.RemoveAll(second)
		t.Skipf("no temp directory short enough for a unix socket: %q is over %d bytes", filepath.Join(second, "d.sock"), socketRoom)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	return second
}

// startLiveWorkspace starts the daemon golder embeds with the pane policy
// golder's own workspace uses, and waits for its first pane to write down the
// environment the daemon stamped on it. The token is therefore the daemon's
// real token for that pane, not a stand-in.
func startLiveWorkspace(t *testing.T) *liveWorkspace {
	t.Helper()
	dir := tempDir(t, "gws-live-")
	socket := filepath.Join(dir, "d.sock")
	envOut := filepath.Join(dir, "pane.env")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := mini.Start(ctx, mini.Config{
		SocketPath: socket,
		Session:    "work",
		Shell:      "/bin/sh",
		Cwd:        dir,
		Width:      100,
		Height:     30,
		Version:    "golder-live-test",
		StateDir:   filepath.Join(dir, "state"),
		LogFile:    filepath.Join(dir, "daemon.log"),
		// The pane writes down what the daemon gave it, then stays alive as a
		// pane rather than exiting and closing.
		PaneEnv: []string{"GWS_ENV_OUT=" + envOut},
		Command: []string{"/bin/sh", "-c", `env > "$GWS_ENV_OUT"; exec sleep 120`},
		// The policy internal/workspace starts golder's own workspace with.
		PermissionsMode: "strict",
		Grants:          []string{"read", "write", "fan"},
	})
	if err != nil {
		t.Fatalf("start the daemon golder embeds: %v", err)
	}
	t.Cleanup(srv.Stop)

	paneEnv := waitForPaneEnv(t, envOut)
	lw := &liveWorkspace{dir: dir, socket: srv.Socket(), session: srv.Session()}
	lw.pane = paneEnv["TUIOS_PANE_ID"]
	lw.token = paneEnv["TUIOS_PANE_TOKEN"]
	if lw.pane == "" || lw.token == "" {
		t.Fatalf("the pane was started without an identity: %v", paneEnv)
	}
	// The identity the daemon stamped is the pane the daemon lists.
	if !containsWindow(t, lw, lw.pane) {
		t.Fatalf("TUIOS_PANE_ID %q is not a window of session %q", lw.pane, lw.session)
	}
	return lw
}

// waitForPaneEnv reads the environment the pane wrote, failing the test rather
// than reading a half-written file.
func waitForPaneEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "TUIOS_PANE_TOKEN=") {
			env := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if name, value, ok := strings.Cut(line, "="); ok {
					env[name] = value
				}
			}
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pane never wrote its environment: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// paneFor returns the environment of a golder running in this workspace's pane,
// as the daemon stamped it.
func (lw *liveWorkspace) paneFor() map[string]string {
	return map[string]string{
		EnvWorkspace:       "1",
		"TUIOS_ENV":        "1",
		"TUIOS_SOCKET":     lw.socket,
		"TUIOS_SESSION":    lw.session,
		"TUIOS_PANE_ID":    lw.pane,
		"TUIOS_PANE_TOKEN": lw.token,
	}
}

// newPane starts a second pane: the neighbour a report must never reach.
func (lw *liveWorkspace) newPane(t *testing.T, name string) string {
	t.Helper()
	c, err := control.Dial(context.Background(), lw.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.Call(context.Background(), "new-window", map[string]any{
		"session": lw.session, "name": name, "command": []string{"/bin/sh", "-c", "exec sleep 120"}, "focus": false,
	})
	if err != nil {
		t.Fatalf("start the neighbour pane: %v", err)
	}
	var out struct {
		WindowID string `json:"window_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.WindowID == "" {
		t.Fatalf("new-window = %s (%v)", raw, err)
	}
	return out.WindowID
}

// agentState is what the daemon records for one window: the state it shows, the
// conversation it holds and what a report said the pane waits for.
type agentState struct {
	State          string            `json:"state"`
	Message        string            `json:"message"`
	BlockedBy      string            `json:"blocked_by"`
	AgentSessionID string            `json:"agent_session_id"`
	Source         string            `json:"source"`
	Meta           map[string]string `json:"meta"`
}

// readAgentState asks the daemon what it holds for one window, over the same
// control protocol the pane tools use. The test process runs in no pane, so it
// reads with the person's own rights, which is what makes it a witness.
func (lw *liveWorkspace) readAgentState(t *testing.T, window string) agentState {
	t.Helper()
	c, err := control.Dial(context.Background(), lw.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.Call(context.Background(), "get-agent-state", map[string]any{
		"session": lw.session, "window": window,
	})
	if err != nil {
		t.Fatalf("get-agent-state %s: %v", window, err)
	}
	var got agentState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("get-agent-state = %s (%v)", raw, err)
	}
	return got
}

// waitForState polls the daemon until the window shows what want says, and
// fails with what it does show rather than sleeping a fixed time.
func (lw *liveWorkspace) waitForState(t *testing.T, window string, want func(agentState) bool, why string) agentState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got agentState
	for {
		got = lw.readAgentState(t, window)
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never showed %s for %s: %+v", why, window, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLiveWorkspaceReportsItsOwnPane: a golder running in a real pane reports
// its state, the approval it waits on, its metadata and its conversation, and
// the daemon records all of it against that pane — behind a connection it had
// to give up every other pane for first.
func TestLiveWorkspaceReportsItsOwnPane(t *testing.T) {
	lw := startLiveWorkspace(t)
	neighbour := lw.newPane(t, "neighbour")

	var log syncBuffer
	sess := OpenSession(Options{Getenv: env(lw.paneFor()), ErrLog: &log, Timeout: 5 * time.Second})
	if sess == nil {
		t.Fatal("OpenSession returned nil inside a real workspace pane")
	}
	defer sess.Close()
	if got := sess.HostName(); got != MuxGolder {
		t.Fatalf("HostName = %q, want %q", got, MuxGolder)
	}

	// A session start is the conversation plus rest, then a turn begins.
	const id = "20261011-231500-000123"
	sess.Start(id, ResumeArgv(id))
	sess.TurnStart()
	got := lw.waitForState(t, lw.pane, func(s agentState) bool {
		return s.State == "working" && s.AgentSessionID == id
	}, "the pane working on its own conversation")
	if got.Source != workspaceSource {
		t.Errorf("source = %q, want %q", got.Source, workspaceSource)
	}

	// An approval: the state says what it waits for, and the kind reads back in
	// the daemon's own vocabulary.
	unblock := sess.Block(KindApproval, "approve Bash: rm -rf build")
	got = lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "needs_input" },
		"the pane waiting for the person")
	if got.Message != "approve Bash: rm -rf build" {
		t.Errorf("message = %q, want the approval it waits for", got.Message)
	}
	if got.BlockedBy != string(KindApproval) {
		t.Errorf("blocked_by = %q, want %q", got.BlockedBy, KindApproval)
	}
	// The conversation is still the pane's: a state report carries it.
	if got.AgentSessionID != id {
		t.Errorf("agent_session_id = %q, want %q", got.AgentSessionID, id)
	}

	// Metadata is the daemon's own tokens, under the keys both hosts share.
	sess.Meta(map[string]string{MetaModel: "deepseek-v4.1-flash", MetaContext: "42%"})
	got = lw.waitForState(t, lw.pane, func(s agentState) bool { return s.Meta[MetaModel] != "" }, "the pane's metadata")
	if got.Meta[MetaModel] != "deepseek-v4.1-flash" || got.Meta[MetaContext] != "42%" {
		t.Errorf("meta = %v", got.Meta)
	}

	// Answering the prompt returns the pane to work while the turn is open, and
	// the finished turn is "done", still on the same conversation.
	unblock()
	lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "working" }, "the pane back at work")
	sess.TurnEnd(false)
	got = lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "done" }, "a finished turn")
	if got.AgentSessionID != id {
		t.Errorf("agent_session_id after the turn = %q, want %q", got.AgentSessionID, id)
	}

	// The neighbour was never touched: no state, no conversation, no metadata.
	if nb := lw.readAgentState(t, neighbour); nb.State != "none" || nb.AgentSessionID != "" || len(nb.Meta) != 0 {
		t.Errorf("the neighbour pane was reported on: %+v", nb)
	}

	// Going away leaves a shell behind, not an agent.
	sess.Close()
	lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "none" }, "the pane released")
	if log.String() != "" {
		t.Errorf("a working report logged an error: %q", log.String())
	}
}

// TestLiveWorkspaceRefusesAProofThatIsNotItsOwn is the security half, run
// against the daemon rather than a fake: a same-UID process outside every pane
// that claims a neighbour's window — with a token it made up, or with no token
// at all — gets no report through, and the neighbour is left alone.
func TestLiveWorkspaceRefusesAProofThatIsNotItsOwn(t *testing.T) {
	lw := startLiveWorkspace(t)
	neighbour := lw.newPane(t, "neighbour")

	cases := []struct {
		name string
		kv   func() map[string]string
	}{
		{
			name: "a token that is not that pane's",
			kv: func() map[string]string {
				kv := lw.paneFor()
				kv["TUIOS_PANE_ID"] = neighbour
				kv["TUIOS_PANE_TOKEN"] = strings.Repeat("0", 32)
				return kv
			},
		},
		{
			name: "another pane's real token for a pane this process is not in",
			kv: func() map[string]string {
				// The token is genuine — but the daemon holds this connection to
				// the pane it proves, so a report about the neighbour is refused.
				kv := lw.paneFor()
				kv["TUIOS_PANE_ID"] = neighbour
				return kv
			},
		},
		{
			name: "no token at all from outside every pane",
			kv: func() map[string]string {
				kv := lw.paneFor()
				kv["TUIOS_PANE_ID"] = neighbour
				delete(kv, "TUIOS_PANE_TOKEN")
				return kv
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log syncBuffer
			rep := Open(Options{Getenv: env(tc.kv()), ErrLog: &log, Timeout: 5 * time.Second})
			if rep == nil {
				t.Fatal("Open returned nil; reporting is decided by the environment, not by the daemon")
			}
			err := rep.host.Apply(context.Background(), Report{State: StateWorking}, SessionRef{ID: "20261011-231500-000123"})
			if err == nil {
				t.Fatal("a report with no proof of its own pane was accepted")
			}
			// The same report through the driver's handle is dropped and
			// logged, not retried: an agent mid-turn has nothing to do with it.
			rep.State(StateWorking, KindNone, "")
			rep.Close()
			// Whatever the daemon did, it did not put a state on the neighbour.
			if nb := lw.readAgentState(t, neighbour); nb.State != "none" || nb.AgentSessionID != "" {
				t.Fatalf("a refused report reached the neighbour pane: %+v", nb)
			}
			if !strings.Contains(log.String(), "host report") {
				t.Errorf("log = %q, want the refusal recorded", log.String())
			}
		})
	}
}

// TestLiveWorkspaceRestrictionHoldsOnTheDaemon pins what own scope and
// read_only mean when the daemon enforces them, because that is what the host
// relies on instead of a `tuios` binary's idea of the caller.
func TestLiveWorkspaceRestrictionHoldsOnTheDaemon(t *testing.T) {
	lw := startLiveWorkspace(t)
	neighbour := lw.newPane(t, "neighbour")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// The good proof: a connection that presents the pane's own token is held
	// to that pane, and can report for it.
	own, err := tuiosctl.Dial(ctx, tuiosctl.Options{
		Socket: lw.socket, Session: lw.session, PaneID: lw.pane, Proof: lw.token, Version: "golder-live-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	if err := own.Restrict(ctx, false); err != nil {
		t.Fatalf("a real pane token was refused: %v", err)
	}
	if _, err := own.Call(ctx, "set-agent-state", map[string]any{
		"window": lw.pane, "session": lw.session, "state": "working", "source": workspaceSource, "harness": Harness,
	}); err != nil {
		t.Fatalf("a restricted connection could not report its own pane: %v", err)
	}
	lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "working" }, "its own pane working")
	// And it cannot report about the neighbour, whatever it names.
	_, err = own.Call(ctx, "set-agent-state", map[string]any{
		"window": neighbour, "session": lw.session, "state": "errored", "source": workspaceSource, "harness": Harness,
	})
	var refused *tuiosctl.Error
	if !errors.As(err, &refused) || refused.Code != "forbidden" {
		t.Fatalf("a restricted connection reported on a neighbour: %v", err)
	}
	if nb := lw.readAgentState(t, neighbour); nb.State != "none" {
		t.Fatalf("the neighbour was reported on: %+v", nb)
	}

	// The bad proof is refused outright by the daemon, which is what lets the
	// host fail closed instead of reporting with the person's own rights.
	bad, err := tuiosctl.Dial(ctx, tuiosctl.Options{
		Socket: lw.socket, Session: lw.session, PaneID: neighbour, Proof: "not-the-token", Version: "golder-live-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	if err := bad.Restrict(ctx, false); err == nil {
		t.Fatal("a made-up token was accepted as proof of a pane")
	}

	// read_only: the same real token, giving up the ability to type.
	read, err := tuiosctl.Dial(ctx, tuiosctl.Options{
		Socket: lw.socket, Session: lw.session, PaneID: lw.pane, Proof: lw.token, Version: "golder-live-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if err := read.Restrict(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := read.SendText(ctx, lw.pane, "echo must-not-land\n", false); !errors.As(err, &refused) || refused.Code != "forbidden" {
		t.Fatalf("a read-only connection typed into a pane: %v", err)
	}
	// A report about its own pane is still allowed: read_only refuses what
	// types, not what reports.
	if _, err := read.Call(ctx, "set-agent-state", map[string]any{
		"window": lw.pane, "session": lw.session, "state": "idle", "source": workspaceSource, "harness": Harness,
	}); err != nil {
		t.Fatalf("a read-only connection could not report its own pane: %v", err)
	}
}

// TestLiveWorkspaceNestedRunKeepsThePane is the sub-agent case. golder starts a
// child golder inside its own pane and hands it the pane identity variables
// (internal/runtime's subAgentEnv does exactly this, after the credential
// scrub). That child is in the pane, so its connection is held to it, and the
// conversation it reports is refused as a nested run's — the pane keeps the
// agent that owns it.
func TestLiveWorkspaceNestedRunKeepsThePane(t *testing.T) {
	lw := startLiveWorkspace(t)

	// The pane's own agent first, working on its conversation.
	var log syncBuffer
	parent := Open(Options{Getenv: env(lw.paneFor()), ErrLog: &log, Timeout: 5 * time.Second})
	if parent == nil {
		t.Fatal("Open returned nil inside a real workspace pane")
	}
	defer parent.Close()
	const parentID = "20261011-231500-000123"
	parent.SessionState(parentID, ResumeArgv(parentID), StateWorking, KindNone, "")
	lw.waitForState(t, lw.pane, func(s agentState) bool {
		return s.State == "working" && s.AgentSessionID == parentID
	}, "the pane's own conversation")

	// The nested golder, with no variable beyond the pane identity it was
	// given: it is a child of the test process, so the kernel places it in no
	// pane and only the token can.
	out := filepath.Join(lw.dir, "nested.out")
	stop := filepath.Join(lw.dir, "nested.stop")
	// A fresh copy of this test binary: the nested golder.
	cmd := &exec.Cmd{Path: os.Args[0], Args: []string{os.Args[0], "-test.run=^$"}}
	cmd.Env = nestedEnv(lw)
	cmd.Env = append(cmd.Env, liveHelperEnv+"="+out, liveStopEnv+"="+stop)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o600)
		_ = cmd.Wait()
	})
	waitForFile(t, out)
	if data, err := os.ReadFile(out); err == nil && strings.Contains(string(data), "no host") {
		t.Fatalf("the nested golder found no host to report to: %s", data)
	}

	// The nested run's report landed on the pane — that is its state, and it is
	// the same pane — but the pane's conversation stayed the parent's: a child
	// cannot take the pane over.
	lw.waitForState(t, lw.pane, func(s agentState) bool { return s.State == "working" }, "the nested run's state")
	lw.waitForState(t, lw.pane, func(s agentState) bool { return s.AgentSessionID == parentID }, "the parent's conversation")
}

// nestedEnv is the environment a nested golder inherits: the pane identity and
// nothing else, which is what internal/runtime's sub-agent scrub leaves.
func nestedEnv(lw *liveWorkspace) []string {
	inherited := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + lw.dir,
		"TMPDIR=" + lw.dir,
		EnvWorkspace + "=1",
		"TUIOS_ENV=1",
		"TUIOS_SOCKET=" + lw.socket,
		"TUIOS_SESSION=" + lw.session,
		"TUIOS_PANE_ID=" + lw.pane,
		"TUIOS_PANE_TOKEN=" + lw.token,
	}
	return inherited
}

// waitForFile waits until a file exists and says something, failing rather than
// reading a half-written one.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			// The writer writes once and closes; wait for the newline it ends
			// with so a truncated read cannot be mistaken for the whole line.
			if strings.HasSuffix(string(data), "\n") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the nested golder never reported (waiting for %s)", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// containsWindow reports whether the daemon lists window as a pane of lw's
// session.
func containsWindow(t *testing.T, lw *liveWorkspace, window string) bool {
	t.Helper()
	c, err := control.Dial(context.Background(), lw.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.Call(context.Background(), "list-windows", map[string]any{"session": lw.session})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Windows []struct {
			WindowID string `json:"window_id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	for _, w := range list.Windows {
		if w.WindowID == window {
			return true
		}
	}
	return false
}

// DescribePane is the helper's one-line description of the identity it found,
// so a helper that opens no host says which variable was missing.
func DescribePane(environ []string) string {
	res := Resolve(func(name string) string {
		for _, entry := range environ {
			if k, v, ok := strings.Cut(entry, "="); ok && k == name {
				return v
			}
		}
		return ""
	})
	return fmt.Sprintf("multiplexer=%q candidate=%q missing=%v", res.Multiplexer, res.Candidate, res.Missing)
}
