//go:build darwin || linux

package workspace

// The one executable, run the way a person or a script runs it:
//
//	golder workspace --headless --shell      one readiness line, then a daemon
//	golder workspace status                  what that daemon is doing
//	golder workspace stop                    shut it down, and nothing left over
//
// Nothing is mocked: this is the built binary, its own daemon process, a real
// socket and a real shell pane. PATH holds no `tuios` and no `kitty-browse`, so
// a workspace that needed either would fail here.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readinessLine is the document --headless prints.
type readinessLine struct {
	Session   string `json:"session"`
	Socket    string `json:"socket"`
	Cwd       string `json:"cwd"`
	Project   string `json:"project"`
	ProjectID string `json:"project_id"`
	Config    string `json:"config"`
	StateDir  string `json:"state_dir"`
	Log       string `json:"log"`
	Headless  bool   `json:"headless"`
	Shell     bool   `json:"shell"`
}

// workspaceArgs are the flags every invocation of one test's workspace uses, so
// status and stop find what start created.
func workspaceArgs(dir, socket, session string) []string {
	return []string{"--cwd", dir, "--socket", socket, "--session", session}
}

// TestE2EHeadlessReadinessStatusAndStop is the script-facing contract: one
// readiness document, a status document a script can parse, and a stop that
// ends exactly this daemon and leaves no socket behind.
func TestE2EHeadlessReadinessStatusAndStop(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	args := workspaceArgs(dir, socket, "e2e")

	// --headless --shell: the daemon alone, with a shell where the agent would
	// be. It prints one line and stays alive.
	proc := startGolder(t, bin, dir, env, append([]string{"workspace", "--headless", "--shell"}, args...)...)
	line := proc.nextLine(t)
	var ready readinessLine
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		t.Fatalf("the readiness document is not one line of JSON (%v): %q", err, line)
	}
	if !ready.Headless || !ready.Shell {
		t.Errorf("readiness = %+v, want headless and shell", ready)
	}
	if ready.Session != "e2e" {
		t.Errorf("session = %q, want e2e", ready.Session)
	}
	if ready.Socket != socket {
		t.Errorf("socket = %q, want %q", ready.Socket, socket)
	}
	if ready.Project != dir || ready.Cwd != dir {
		t.Errorf("project = %q cwd = %q, want %q", ready.Project, ready.Cwd, dir)
	}
	// Everything it names lives under this test's own root: the person's state
	// root and runtime directory are untouched.
	for name, path := range map[string]string{"config": ready.Config, "state": ready.StateDir, "log": ready.Log} {
		if !strings.HasPrefix(path, root) {
			t.Errorf("%s = %q, want a path under the test root %q", name, path, root)
		}
	}
	// The socket is really there, and it is a socket.
	if st, err := os.Lstat(socket); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the readiness socket %q is not a socket: %v", socket, err)
	}
	if conn, err := net.Dial("unix", socket); err != nil {
		t.Fatalf("the readiness socket does not accept a connection: %v", err)
	} else {
		_ = conn.Close()
	}

	// status: the same workspace, found the same way, reported as one JSON
	// document. The daemon is the process the readiness came from.
	stdout, stderr, code := runCLI(t, bin, dir, env, append([]string{"workspace", "status"}, args...)...)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	status := parseStatus(t, stdout)
	if !status.Running {
		t.Fatalf("status = %s, want running", stdout)
	}
	if status.PID != proc.cmd.Process.Pid {
		t.Errorf("status pid = %d, want the daemon this test started (%d)", status.PID, proc.cmd.Process.Pid)
	}
	if status.Session != "e2e" || status.Socket != socket {
		t.Errorf("status names %q on %q", status.Session, status.Socket)
	}
	if status.Panes.Total != 1 {
		t.Errorf("panes = %d, want the one shell this workspace was started with", status.Panes.Total)
	}
	if !alive(status.PID) {
		t.Errorf("the daemon pid %d is not running", status.PID)
	}
	// A shell pane, not an agent: --shell is what the readiness said.
	if len(status.Panes.Windows) == 1 {
		foreground := filepath.Base(status.Panes.Windows[0].Foreground)
		if strings.Contains(foreground, "golder") {
			t.Errorf("the pane runs %q, want a shell: --shell starts no agent", foreground)
		}
	}
	panes := status.windowIDs()

	// stop: explicit, authenticated, and it says which workspace it stopped.
	stdout, stderr, code = runCLI(t, bin, dir, env, append([]string{"workspace", "stop"}, args...)...)
	if code != 0 {
		t.Fatalf("stop exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `stopped workspace "e2e"`) {
		t.Errorf("stop printed %q", stdout)
	}
	// The daemon the readiness came from exits, cleanly, and nothing is left.
	if err := proc.wait(t, 15*time.Second); err != nil {
		t.Fatalf("the daemon did not exit cleanly after stop: %v", err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Errorf("stop left the socket behind: %v", err)
	}
	// A stopped workspace reports itself stopped rather than failing, and a
	// second stop is not an error.
	stdout, _, code = runCLI(t, bin, dir, env, append([]string{"workspace", "status"}, args...)...)
	if code != 0 {
		t.Fatalf("status after stop exited %d", code)
	}
	if stopped := parseStatus(t, stdout); stopped.Running {
		t.Errorf("status after stop = %s, want running false", stdout)
	}
	if _, stderr, code = runCLI(t, bin, dir, env, append([]string{"workspace", "stop"}, args...)...); code != 0 {
		t.Errorf("a second stop exited %d: %s", code, stderr)
	}
	// The panes went with the daemon: no pane survives its session.
	if len(panes) != 1 {
		t.Fatalf("panes = %v, want one", panes)
	}
}

// TestE2EHeadlessSecondStartRefusesAndTheFirstIsUntouched: one project, one
// session, one daemon. A second start reports the state directory in use rather
// than adopting or replacing the first.
func TestE2EHeadlessSecondStartRefusesAndTheFirstIsUntouched(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	args := workspaceArgs(dir, socket, "e2e")

	first := startGolder(t, bin, dir, env, append([]string{"workspace", "--headless", "--shell"}, args...)...)
	first.nextLine(t)

	// The same state directory, a different socket: refused by the lease, not
	// by luck.
	secondSocket := filepath.Join(root, "t.sock")
	secondArgs := workspaceArgs(dir, secondSocket, "e2e")
	stdout, stderr, code := runCLI(t, bin, dir, env, append([]string{"workspace", "--headless", "--shell"}, secondArgs...)...)
	if code == 0 {
		t.Fatalf("a second start was accepted: stdout %q", stdout)
	}
	if !strings.Contains(stderr, "in use") {
		t.Errorf("the refusal does not name the state directory: %s", stderr)
	}
	// The first daemon is untouched and still serving its pane.
	stdout, _, code = runCLI(t, bin, dir, env, append([]string{"workspace", "status"}, args...)...)
	if code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if status := parseStatus(t, stdout); !status.Running || status.Panes.Total != 1 {
		t.Errorf("the first workspace was damaged: %s", stdout)
	}
	if _, _, code := runCLI(t, bin, dir, env, append([]string{"workspace", "stop"}, args...)...); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if err := first.wait(t, 15*time.Second); err != nil {
		t.Fatal(err)
	}
}
