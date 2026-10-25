//go:build darwin || linux

package workspace

// The visible workspace on a real PTY, through the built executable.
//
// The claim this test exists for is the one a headless run cannot show: what a
// person sees is a *client* of a daemon that outlives it. So the test draws the
// UI in a pty, types into the pane it is showing, detaches with the leader key,
// checks that the daemon and its pane are still there, attaches again, detaches
// again, and only then stops the workspace — and checks that the terminal it
// borrowed was handed back.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// terminal is one pty: the master the test reads and writes, and the slave the
// client is handed as its terminal.
type terminal struct {
	master *os.File
	slave  *os.File
}

// newTerminal opens a pty of the size a workspace accepts.
func newTerminal(t *testing.T) *terminal {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		// A sandbox with no /dev/ptmx is not this package's failure, but it is
		// a capability the test did not exercise: it says so rather than
		// passing quietly.
		t.Skipf("this environment has no pty, so the visible workspace was not run: %v", err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatalf("size the pty: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	return &terminal{master: master, slave: slave}
}

// restoreSequences are the bytes that put a terminal back the way the client
// found it, and every one of them is written on the way out: the alternate
// screen is left, and the DECSTR soft reset the terminal package emits turns
// mouse tracking off, shows the cursor and resets text attributes. A client
// that took the screen and did not give it back writes the entering half and
// none of this.
var restoreSequences = []string{"\x1b[?1049l", "\x1b[!p", "\x1b[?25h", "\x1b[0m"}

// visible is one attached client running in a terminal.
type visible struct {
	cmd  *exec.Cmd
	term *terminal
	draw *lockedBuffer
	done chan struct{}
	err  error
}

// start runs a client in this terminal, in a session of its own so that it has
// a controlling terminal of its own.
func (term *terminal) start(t *testing.T, bin, dir string, env []string, args ...string) *visible {
	t.Helper()
	v := &visible{term: term, draw: &lockedBuffer{}, done: make(chan struct{})}
	v.cmd = exec.Command(bin, args...)
	v.cmd.Dir = dir
	v.cmd.Env = env
	v.cmd.Stdin, v.cmd.Stdout, v.cmd.Stderr = term.slave, term.slave, term.slave
	// A controlling terminal, as a person's shell has: the client asks the
	// terminal for its size and puts it in raw mode, and both need one.
	v.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := v.cmd.Start(); err != nil {
		t.Fatalf("start the visible workspace: %v", err)
	}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := term.master.Read(buf)
			if n > 0 {
				_, _ = v.draw.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		v.err = v.cmd.Wait()
		close(v.done)
	}()
	t.Cleanup(func() {
		select {
		case <-v.done:
		default:
			_ = v.cmd.Process.Kill()
			<-v.done
		}
	})
	return v
}

// output is what the client has drawn so far.
func (v *visible) output() string { return v.draw.String() }

// typeKeys writes to the terminal, as a person typing would.
func (v *visible) typeKeys(t *testing.T, s string) {
	t.Helper()
	if _, err := v.term.master.Write([]byte(s)); err != nil {
		t.Fatalf("write to the pty: %v", err)
	}
}

// waitForOutput waits until what the client drew contains want.
func (v *visible) waitForOutput(t *testing.T, want string, budget time.Duration, what string) {
	t.Helper()
	waitFor(t, budget, what, func() bool { return strings.Contains(v.output(), want) })
}

// waitExit waits for the client to exit and returns its error.
func (v *visible) waitExit(t *testing.T, budget time.Duration) error {
	t.Helper()
	select {
	case <-v.done:
		return v.err
	case <-time.After(budget):
		t.Fatalf("the visible workspace did not exit within %s; last output:\n%s", budget, tailOf(v.output(), 2000))
		return nil
	}
}

// tailOf is the last n bytes of s, for a failure message that does not print a
// megabyte of escape sequences.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestE2EVisibleWorkspaceDetachReattachAndStop is the visible half of the
// contract, on a real pty: start, run a command in the pane, detach, keep,
// re-attach, detach, stop, and give the terminal back each time.
func TestE2EVisibleWorkspaceDetachReattachAndStop(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	args := workspaceArgs(dir, socket, "visible")

	// A client that is killed leaves the daemon it spawned running, which is
	// what a detached workspace is; a test that fails before its stop step must
	// not leave that behind either.
	t.Cleanup(func() { stopQuietly(bin, dir, env, "--session", "visible") })

	firstTerm := newTerminal(t)

	first := firstTerm.start(t, bin, dir, env, append([]string{"workspace", "--shell"}, args...)...)
	// A drawn frame: the alternate screen and the frame bracket bubbletea
	// writes around a whole frame. A client that connected and rendered
	// nothing writes neither.
	first.waitForOutput(t, "\x1b[?1049h", 60*time.Second, "the visible workspace to take the screen")
	first.waitForOutput(t, "\x1b[?2026h", 60*time.Second, "the visible workspace to draw a frame")

	// The pane is a live shell: a command typed at the terminal runs in it. The
	// arithmetic keeps the typed line and the result different strings, so
	// finding the result cannot be the terminal echoing the keystrokes back.
	first.typeKeys(t, "echo visible-$((1+1))-ok\r")
	first.waitForOutput(t, "visible-2-ok", 30*time.Second, "the pane to run what was typed at it")

	// The daemon behind the client, found the way a script finds it.
	stdout, stderr, code := runCLI(t, bin, dir, env, "workspace", "status", "visible")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	attached := parseStatus(t, stdout)
	if !attached.Running || attached.Panes.Total != 1 {
		t.Fatalf("status while attached = %s", stdout)
	}
	if attached.PID == first.cmd.Process.Pid {
		t.Errorf("the daemon is the client process (%d); it must be a process of its own", attached.PID)
	}
	panes := attached.windowIDs()

	// Detach: the leader key, then d. The client goes, the workspace stays.
	first.typeKeys(t, "\x02d")
	if err := first.waitExit(t, 30*time.Second); err != nil {
		t.Fatalf("detaching failed: %v (output tail:\n%s)", err, tailOf(first.output(), 2000))
	}
	// The terminal is given back, not merely left: every sequence that undoes
	// what the client did to it is in what it wrote.
	first.waitForOutput(t, restoreSequences[0], 10*time.Second, "the client to leave the alternate screen")
	for _, seq := range restoreSequences[1:] {
		if !strings.Contains(first.output(), seq) {
			t.Errorf("the client did not write the %q part of its terminal reset; output tail:\n%s",
				seq, tailOf(first.output(), 2000))
		}
	}

	// The daemon and its pane survived the detach.
	stdout, _, code = runCLI(t, bin, dir, env, "workspace", "status", "visible")
	if code != 0 {
		t.Fatalf("status after detach exited %d", code)
	}
	detached := parseStatus(t, stdout)
	if !detached.Running {
		t.Fatalf("the workspace did not survive the detach: %s", stdout)
	}
	if detached.PID != attached.PID {
		t.Errorf("the daemon changed across a detach: %d then %d", attached.PID, detached.PID)
	}
	if got := detached.windowIDs(); strings.Join(got, ",") != strings.Join(panes, ",") {
		t.Errorf("the panes changed across a detach: %v then %v", panes, got)
	}
	if !alive(detached.PID) {
		t.Errorf("the daemon pid %d is not running after a detach", detached.PID)
	}

	// Re-attach: no --shell and no agent arguments, which is what attaching to a
	// running workspace means. The pane that was left is the pane drawn.
	second := newTerminal(t).start(t, bin, dir, env, "workspace", "attach", "visible")
	second.waitForOutput(t, "\x1b[?1049h", 60*time.Second, "the re-attached client to take the screen")
	second.typeKeys(t, "echo again-$((2+2))-ok\r")
	second.waitForOutput(t, "again-4-ok", 30*time.Second, "the re-attached pane to run a command")
	second.typeKeys(t, "\x02d")
	if err := second.waitExit(t, 30*time.Second); err != nil {
		t.Fatalf("detaching the second time failed: %v", err)
	}
	for _, seq := range restoreSequences {
		if !strings.Contains(second.output(), seq) {
			t.Errorf("the second client did not write %q on its way out; output tail:\n%s",
				seq, tailOf(second.output(), 2000))
		}
	}

	// An explicit stop ends the daemon, the panes and the socket.
	stdout, stderr, code = runCLI(t, bin, dir, env, "workspace", "stop", "visible")
	if code != 0 {
		t.Fatalf("stop exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, `stopped workspace "visible"`) {
		t.Errorf("stop printed %q", stdout)
	}
	waitFor(t, 15*time.Second, "the daemon to exit after stop", func() bool { return !alive(detached.PID) })
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Errorf("stop left the socket behind: %v", err)
	}
	stdout, _, code = runCLI(t, bin, dir, env, "workspace", "status", "visible")
	if code != 0 {
		t.Fatalf("status after stop exited %d", code)
	}
	if stopped := parseStatus(t, stdout); stopped.Running {
		t.Errorf("status after stop = %s, want running false", stdout)
	}
}

// TestE2EVisibleWorkspaceNeedsATerminal: the visible workspace refuses a pipe
// and names the mode to use instead, rather than drawing into nothing.
func TestE2EVisibleWorkspaceNeedsATerminal(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")

	_, stderr, code := runCLI(t, bin, dir, env,
		append([]string{"workspace", "--shell"}, workspaceArgs(dir, socket, "visible")...)...)
	if code != 1 {
		t.Fatalf("a visible workspace on a pipe exited %d, want 1", code)
	}
	if !strings.Contains(stderr, "--headless") {
		t.Errorf("the refusal does not name --headless: %s", stderr)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Errorf("a refused start left a socket: %v", err)
	}
}
