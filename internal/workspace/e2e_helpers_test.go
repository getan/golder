//go:build darwin || linux

package workspace

// Helpers for the end-to-end tests: one real golder executable, one isolated
// environment, one short private root, and a way to run a command and read its
// first line.
//
// Two things are deliberate. Every path a test touches is derived from a
// temporary root, so a test can never read or write the person's GOLDER_HOME,
// their runtime directory, their browser profile or their config. And the
// executable under test is built from this checkout rather than assumed on
// PATH: `golder workspace` spawns *itself* for the daemon, so which binary is
// being tested is not a detail.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// paneDumpEnv turns the test binary into the first pane's agent: it writes down
// the argv it was run with and the environment it was given, then stays alive
// as a pane does. See e2e_agent_pane_test.go.
const paneDumpEnv = "GOLDER_WORKSPACE_E2E_PANE_DUMP"

// TestMain is also the entry point of the pane agent described above; a child
// started with paneDumpEnv set is not a test run.
func TestMain(m *testing.M) {
	if out := os.Getenv(paneDumpEnv); out != "" {
		os.Exit(dumpPane(out))
	}
	code := m.Run()
	// One build is shared by every test in the run, so it is removed once the
	// run is over rather than when the first test that needed it ends.
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// dumpPane is the pane's first agent: it records its argv and environment and
// then waits, the way an agent waits at its prompt.
func dumpPane(out string) int {
	dump := struct {
		Argv []string `json:"argv"`
		Env  []string `json:"env"`
		Cwd  string   `json:"cwd"`
	}{Argv: os.Args, Env: os.Environ()}
	dump.Cwd, _ = os.Getwd()
	body, err := json.Marshal(dump)
	if err != nil {
		return 1
	}
	if err := os.WriteFile(out, append(body, '\n'), 0o600); err != nil {
		return 1
	}
	// A pane is a process that stays: exiting would close the pane and take the
	// session with it.
	time.Sleep(2 * time.Minute)
	return 0
}

// lockedBuffer is a bytes.Buffer safe for a child's output goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// tempRoot is a short private directory for sockets and state: the platform's
// temp root first, /tmp second, and a skip that names the socket limit rather
// than a mystery failure when neither is short enough for sockaddr_un.
func tempRoot(t *testing.T) string {
	t.Helper()
	const socketRoom = 92 // the workspace command's own limit for --socket
	try := func(root string) (string, error) {
		if root == "" {
			return os.MkdirTemp("", "gws-e2e-")
		}
		return os.MkdirTemp(root, "gws-e2e-")
	}
	first, firstErr := try("")
	if firstErr == nil && len(filepath.Join(first, "s.sock")) <= socketRoom {
		t.Cleanup(func() { _ = os.RemoveAll(first) })
		return canonical(t, first)
	}
	if first != "" {
		_ = os.RemoveAll(first)
	}
	second, secondErr := try("/tmp")
	if secondErr != nil {
		t.Fatalf("no temp directory for a workspace socket (temp root: %v; /tmp: %v)", firstErr, secondErr)
	}
	if len(filepath.Join(second, "s.sock")) > socketRoom {
		_ = os.RemoveAll(second)
		t.Skipf("no temp directory short enough for a workspace socket: %q is over %d bytes", filepath.Join(second, "s.sock"), socketRoom)
	}
	t.Cleanup(func() { _ = os.RemoveAll(second) })
	return canonical(t, second)
}

// canonical resolves a path the way the workspace command does: /var is a
// symlink to /private/var on macOS, and the project identity is the resolved
// path. A test that compared the two spellings would fail on a correct run.
func canonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %q: %v", path, err)
	}
	return resolved
}

// isolatedEnv is the environment a test's golder runs with: a home, a state
// root, a runtime directory, the XDG directories and a PATH with nothing
// installed on it. It inherits nothing from the test process that could reach
// the person's own files or a real multiplexer.
func isolatedEnv(t *testing.T, root string) []string {
	t.Helper()
	dirs := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"XDG_RUNTIME_DIR": filepath.Join(root, "run"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg", "config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg", "cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "xdg", "state"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg", "data"),
		"TMPDIR":          filepath.Join(root, "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// An empty PATH is the point: no `tuios`, no `kitty-browse`, nothing this
	// workspace could shell out to. The pane's shell and the executable are
	// named by absolute path.
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + dirs["HOME"],
		"GOLDER_HOME=" + filepath.Join(root, "golder"),
		"XDG_RUNTIME_DIR=" + dirs["XDG_RUNTIME_DIR"],
		"XDG_CONFIG_HOME=" + dirs["XDG_CONFIG_HOME"],
		"XDG_CACHE_HOME=" + dirs["XDG_CACHE_HOME"],
		"XDG_STATE_HOME=" + dirs["XDG_STATE_HOME"],
		"XDG_DATA_HOME=" + dirs["XDG_DATA_HOME"],
		"TMPDIR=" + dirs["TMPDIR"],
		"PATH=" + bin,
		"SHELL=/bin/sh",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
	}
	// A test run that happens to be inside a multiplexer must not make the
	// workspace refuse to start as a nested one; and no reporting variable may
	// point at a daemon that is not the test's.
	for _, name := range []string{"TUIOS_ENV", "TUIOS_SOCKET", "TUIOS_SESSION", "TUIOS_PANE_ID", "TUIOS_PANE_TOKEN", "GOLDER_WORKSPACE", "TMUX", "STY", "ZELLIJ", "HERDR_ENV"} {
		env = append(env, name+"=")
	}
	return env
}

// project makes the checkout a workspace is opened on.
func project(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// golderBinary builds the executable this checkout produces, once per test
// run, and returns its path.
func golderBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		// A short run keeps `go test ./...` to the tests that need no daemon:
		// these build an executable, start panes and attach a client.
		t.Skip("the end-to-end workspace tests are skipped by -short: they build the executable and run real daemons")
	}
	buildOnce.Do(func() {
		root, err := os.MkdirTemp("", "golder-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = root
		bin := filepath.Join(root, "golder")
		cmd := exec.Command("go", "build", "-o", bin, "github.com/getan/golder/cmd/golder")
		cmd.Dir = moduleRoot()
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/golder: %v\n%s", err, out)
			return
		}
		buildPath = bin
	})
	if buildErr != nil {
		t.Fatalf("the executable under test could not be built: %v", buildErr)
	}
	return buildPath
}

var (
	buildOnce sync.Once
	buildDir  string
	buildPath string
	buildErr  error
)

// moduleRoot walks up from the test's package directory to the module root.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// golderProcess is one running golder: its first stdout line, its stderr, and
// the closed channel that says when it exited.
type golderProcess struct {
	cmd    *exec.Cmd
	lines  chan string
	stderr *lockedBuffer
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// startGolder runs the executable with args in dir and starts reading its
// output.
func startGolder(t *testing.T, bin, dir string, env []string, args ...string) *golderProcess {
	t.Helper()
	p := &golderProcess{stderr: &lockedBuffer{}}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Dir = dir
	p.cmd.Env = env
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s %v: %v", bin, args, err)
	}
	p.done = make(chan struct{})
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		// Closed rather than sent: more than one reader asks whether the
		// process is gone, and a value would be read by only one of them.
		close(p.done)
	}()
	p.lines = make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 4<<10), 1<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		// A test that failed before the stop step must not leave a daemon
		// behind: killing the exact process this test started, and only it.
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// nextLine waits for the next line of output, failing rather than hanging.
func (p *golderProcess) nextLine(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			t.Fatalf("the process closed its output without a line; stderr: %s", p.stderr.String())
		}
		return line
	case <-time.After(30 * time.Second):
		t.Fatalf("no output within 30s; stderr: %s", p.stderr.String())
		return ""
	}
}

// wait waits for the process to exit and returns its error.
func (p *golderProcess) wait(t *testing.T, budget time.Duration) error {
	t.Helper()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.err
	case <-time.After(budget):
		t.Fatalf("the process did not exit within %s; stderr: %s", budget, p.stderr.String())
		return nil
	}
}

// runCLI runs the executable to completion and returns its output and exit
// code.
func runCLI(t *testing.T, bin, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out", bin, args)
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("%s %v: %v", bin, args, err)
	}
	return out.String(), errOut.String(), code
}

// stopQuietly asks a workspace to stop and ignores every error.
//
// A test that fails before its own stop step must not leave a daemon behind: a
// visible client that is killed leaves the daemon it spawned running, by
// design. Cleanup uses this rather than the failing helpers, so the reason the
// test failed stays the reported one.
func stopQuietly(bin, dir string, env []string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"workspace", "stop"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	_ = cmd.Run()
}

// waitFor polls check until it holds, failing with why after the budget.
func waitFor(t *testing.T, budget time.Duration, why string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", budget, why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reports whether pid is a live process.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// statusDoc is the workspace status document a script reads.
type statusDoc struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid"`
	Session string `json:"session"`
	Socket  string `json:"socket"`
	Version string `json:"version"`
	Panes   struct {
		Total   int `json:"total"`
		Windows []struct {
			WindowID   string `json:"window_id"`
			Title      string `json:"title"`
			Foreground string `json:"foreground_cmd"`
		} `json:"windows"`
	} `json:"panes"`
}

// parseStatus decodes one status document, failing with the bytes on anything
// that is not JSON.
func parseStatus(t *testing.T, stdout string) statusDoc {
	t.Helper()
	var doc statusDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status is not JSON (%v): %s", err, stdout)
	}
	return doc
}

// windowIDs is the set of panes a status document lists, sorted by the daemon.
func (d statusDoc) windowIDs() []string {
	out := make([]string, 0, len(d.Panes.Windows))
	for _, w := range d.Panes.Windows {
		out = append(out, w.WindowID)
	}
	return out
}
