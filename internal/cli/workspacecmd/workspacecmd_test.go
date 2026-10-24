package workspacecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/golderhome"
)

// recorder is a Backend that records what it was asked and answers with what
// the test set. It is why none of these tests need a daemon, a terminal or a
// binary on PATH: the command is asked for its decisions, not for its effects.
type recorder struct {
	start  func(context.Context, Options) (StartResult, error)
	status func(context.Context, Options) ([]byte, error)
	stop   func(context.Context, Options) error

	started  []Options
	statused []Options
	stopped  []Options
}

func (r *recorder) Start(ctx context.Context, o Options) (StartResult, error) {
	r.started = append(r.started, o)
	if r.start != nil {
		return r.start(ctx, o)
	}
	return StartResult{}, nil
}

func (r *recorder) Status(ctx context.Context, o Options) ([]byte, error) {
	r.statused = append(r.statused, o)
	if r.status != nil {
		return r.status(ctx, o)
	}
	return []byte(`{"session":"x","windows":[]}`), nil
}

func (r *recorder) Stop(ctx context.Context, o Options) error {
	r.stopped = append(r.stopped, o)
	if r.stop != nil {
		return r.stop(ctx, o)
	}
	return nil
}

// calls is how much of the machine a backend was asked to touch: a test that
// expects a refusal wants this to be zero.
func (r *recorder) calls() int { return len(r.started) + len(r.statused) + len(r.stopped) }

// testEnv is one project, one state root and one runtime directory, all inside
// the test's own temp directory, plus the getenv/getwd seams that point at them.
// Nothing here can reach the person's real ~/.golder or their real /run.
type testEnv struct {
	base    string
	project string
	home    string
	runtime string
	kv      map[string]string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	base := shortTempDir(t)
	// The command resolves symlinks, so the test's own idea of "the project"
	// has to be canonical too (macOS's /var is /private/var).
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	e := &testEnv{
		base:    base,
		project: filepath.Join(base, "project"),
		home:    filepath.Join(base, "home"),
		runtime: filepath.Join(base, "run"),
		kv: map[string]string{
			golderhome.EnvHome: base + "/home",
			"XDG_RUNTIME_DIR":  base + "/run",
			// The multiplexer markers are stated, not inherited: a developer
			// running these tests inside tmux must see the same results as CI.
			"TMUX":                "",
			"STY":                 "",
			"ZELLIJ":              "",
			"ZELLIJ_SESSION_NAME": "",
			"TUIOS_ENV":           "",
			"HERDR_ENV":           "",
			"GOLDER_WORKSPACE":    "",
		},
	}
	for _, dir := range []string{e.project, e.home, e.runtime} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("prepare %s: %v", dir, err)
		}
	}
	return e
}

// shortTempDir is a temporary directory whose path is short enough to hold a
// Unix socket. t.TempDir() is named after the test — on macOS that is
// /var/folders/.../T/TestSomethingVeryDescriptive12345/001, which on its own can
// be most of a socket path's hundred bytes — so these tests would be measuring
// the socket length limit rather than the behaviour they are about. The
// limit itself is tested directly in TestSocketTooLongFallsBackToAHash.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gws")
	if err != nil {
		t.Fatalf("create a temporary directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func (e *testEnv) deps() Deps {
	return Deps{
		Getenv: func(k string) string { return e.kv[k] },
		Getwd:  func() (string, error) { return e.project, nil },
	}
}

func (e *testEnv) set(k, v string) { e.kv[k] = v }

// run drives the command with this environment and captures everything it says.
func (e *testEnv) run(t *testing.T, backend Backend, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return e.runCtx(t, context.Background(), backend, args...)
}

func (e *testEnv) runCtx(t *testing.T, ctx context.Context, backend Backend, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = RunWith(ctx, args, &out, &errOut, backend, e.deps())
	return out.String(), errOut.String(), code
}

// TestDefaultStartsAVisibleWorkspace is the headline behaviour: `golder
// workspace` with no arguments starts a workspace a person can see, in this
// project, under the project's own durable directory — and says nothing on
// stdout, because it is about to hand the terminal to the workspace itself.
func TestDefaultStartsAVisibleWorkspace(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}

	stdout, stderr, code := e.run(t, rec)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("a visible start printed %q, and its output is the workspace", stdout)
	}
	if len(rec.started) != 1 || rec.calls() != 1 {
		t.Fatalf("backend calls = start %d, status %d, stop %d; want one start and nothing else",
			len(rec.started), len(rec.statused), len(rec.stopped))
	}

	got := rec.started[0]
	if got.Command != CommandRun {
		t.Errorf("command = %q, want run", got.Command)
	}
	if got.Headless {
		t.Error("headless = true, and a bare `golder workspace` starts a visible workspace")
	}
	if got.Shell {
		t.Error("shell = true, and an agent is the default")
	}
	if got.Cwd != e.project || got.Project != e.project {
		t.Errorf("cwd = %q / project = %q, want %q", got.Cwd, got.Project, e.project)
	}
	for name, path := range map[string]string{
		"config": got.ConfigPath,
		"state":  got.StateDir,
		"log":    got.LogFile,
	} {
		if !strings.HasPrefix(path, e.home) {
			t.Errorf("%s = %q, want it under the state root %q", name, path, e.home)
		}
	}
	// XDG_RUNTIME_DIR is set in this environment, so the private socket directory
	// is the one RuntimeDir names under it.
	if runtimeDir := RuntimeDir(e.deps().Getenv); !strings.HasPrefix(got.Socket, runtimeDir) {
		t.Errorf("socket = %q, want it in the private runtime directory %q", got.Socket, runtimeDir)
	}
	if got.Session == "" || !strings.HasPrefix(got.Session, "golder-project-") {
		t.Errorf("session = %q, want one derived from the project", got.Session)
	}
	if len(got.AgentArgs) != 0 {
		t.Errorf("agent args = %q, want none", got.AgentArgs)
	}
	if got.Host != "" {
		t.Errorf("host = %q, want none in a plain terminal", got.Host)
	}
}

// TestRunWordStartsTheSameWorkspace: `run` and no word are one command spelled
// two ways.
func TestRunWordStartsTheSameWorkspace(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	if _, stderr, code := e.run(t, rec, "run"); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if len(rec.started) != 1 || rec.started[0].Command != CommandRun {
		t.Fatalf("backend calls = %+v, want one run", rec.started)
	}
}

// TestAgentArgsReachTheBackendAsArgv pins the passthrough at the seam that
// matters: what a backend is handed. The words are the same words, in the same
// order, with the shell metacharacters still inert because nothing here runs a
// shell.
func TestAgentArgsReachTheBackendAsArgv(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}

	args := []string{"--", "--model", "opus", "a; rm -rf /", "$(id)", "a b", ""}
	if _, stderr, code := e.run(t, rec, args...); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	want := args[1:]
	got := rec.started[0].AgentArgs
	if len(got) != len(want) {
		t.Fatalf("agent args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("agent arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestShellStartsNoAgent: --shell asks for a shell pane, so the workspace runs
// no agent and has nothing to pass an argv to.
func TestShellStartsNoAgent(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	if _, stderr, code := e.run(t, rec, "--shell"); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	got := rec.started[0]
	if !got.Shell {
		t.Error("shell = false, and --shell asked for one")
	}
	if len(got.AgentArgs) != 0 {
		t.Errorf("agent args = %q, want none", got.AgentArgs)
	}
}

// TestHeadlessPrintsReadiness pins the readiness document: the one output a
// script reads, with the names it reads them under. It is checked by decoding,
// because that is what a caller does with it.
func TestHeadlessPrintsReadiness(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}

	stdout, stderr, code := e.run(t, rec, "--headless")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("readiness document does not end with a newline: %q", stdout)
	}
	var doc readiness
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("readiness document is not JSON: %v\n%s", err, stdout)
	}
	got := rec.started[0]
	if !got.Headless {
		t.Error("headless = false, and --headless asked for the daemon only")
	}
	if doc.Session != got.Session || doc.Socket != got.Socket {
		t.Errorf("readiness session/socket = %q/%q, want %q/%q", doc.Session, doc.Socket, got.Session, got.Socket)
	}
	if doc.Cwd != e.project || doc.Project != e.project || doc.ProjectID != got.ProjectID {
		t.Errorf("readiness cwd/project/id = %q/%q/%q, want %q/%q/%q",
			doc.Cwd, doc.Project, doc.ProjectID, e.project, e.project, got.ProjectID)
	}
	if doc.Config != got.ConfigPath || doc.StateDir != got.StateDir || doc.Log != got.LogFile {
		t.Errorf("readiness paths = %q/%q/%q, want %q/%q/%q",
			doc.Config, doc.StateDir, doc.Log, got.ConfigPath, got.StateDir, got.LogFile)
	}
	if !doc.Headless {
		t.Error("the readiness document does not say the workspace is headless")
	}
	if doc.Shell {
		t.Error("the readiness document says shell, and no --shell was given")
	}
}

// TestHeadlessReportsWhatTheBackendSaid: a daemon that chose its own session
// name (or a socket the runtime chose) is what the readiness document reports.
// A script that reads it must be able to reach the workspace that exists, not
// the one it asked for.
func TestHeadlessReportsWhatTheBackendSaid(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{start: func(context.Context, Options) (StartResult, error) {
		return StartResult{Session: "generated", Socket: filepath.Join(e.base, "chosen.sock")}, nil
	}}

	stdout, stderr, code := e.run(t, rec, "--headless")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	var doc readiness
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Session != "generated" {
		t.Errorf("session = %q, want the one the backend reported", doc.Session)
	}
	if doc.Socket != filepath.Join(e.base, "chosen.sock") {
		t.Errorf("socket = %q, want the one the backend reported", doc.Socket)
	}
}

// TestStatusPrintsTheBackendsDocument: `status` is one JSON document on stdout,
// re-indented for a person reading it, unchanged for a script parsing it.
func TestStatusPrintsTheBackendsDocument(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{status: func(context.Context, Options) ([]byte, error) {
		return []byte(`{"session":"s","panes":[{"id":"p1","agent":"golder"}]}`), nil
	}}

	stdout, stderr, code := e.run(t, rec, "status")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, stdout)
	}
	if doc["session"] != "s" {
		t.Errorf("status = %v, want the backend's document", doc)
	}
	if !strings.Contains(stdout, "\n  \"panes\"") {
		t.Errorf("status was not indented for reading:\n%s", stdout)
	}
	if len(rec.statused) != 1 || rec.statused[0].Command != CommandStatus {
		t.Errorf("backend status calls = %+v, want one", rec.statused)
	}
	if strings.Contains(stdout, "stopped") {
		t.Error("status stopped something")
	}
}

// TestStatusRefusesABrokenDocument: a status command whose output cannot be
// parsed is worse than one that fails, so it fails.
func TestStatusRefusesABrokenDocument(t *testing.T) {
	cases := map[string]func(context.Context, Options) ([]byte, error){
		"empty":    func(context.Context, Options) ([]byte, error) { return nil, nil },
		"not JSON": func(context.Context, Options) ([]byte, error) { return []byte("session: s"), nil },
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t)
			rec := &recorder{status: status}
			stdout, stderr, code := e.run(t, rec, "status")
			if code != ExitFailure {
				t.Fatalf("exit = %d, want 1 (stdout: %q stderr: %q)", code, stdout, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing on a failed status", stdout)
			}
			if !strings.Contains(stderr, "golder workspace status:") {
				t.Errorf("stderr = %q, want it to name the status command", stderr)
			}
		})
	}
}

// TestStopSaysWhatItStopped: the destructive verb names its target, so a person
// who typed the wrong --session can see it.
func TestStopSaysWhatItStopped(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}

	stdout, stderr, code := e.run(t, rec, "stop", "--session", "work")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, `stopped workspace "work"`) {
		t.Errorf("stdout = %q, want it to name the workspace it stopped", stdout)
	}
	if len(rec.stopped) != 1 || rec.stopped[0].Session != "work" {
		t.Errorf("backend stop calls = %+v, want one for session work", rec.stopped)
	}
	if len(rec.started) != 0 {
		t.Error("stop started a workspace")
	}
}

// TestHelpExitsZero: `--help` is a success, and it prints to stdout so it
// composes with a pager.
func TestHelpExitsZero(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"status", "--help"}} {
		rec := &recorder{}
		stdout, stderr, code := e.run(t, rec, args...)
		if code != ExitOK {
			t.Fatalf("Run(%q) exit = %d, want 0", args, code)
		}
		if !strings.Contains(stdout, "usage: golder workspace") {
			t.Errorf("Run(%q) stdout = %q, want the usage", args, stdout)
		}
		if stderr != "" {
			t.Errorf("Run(%q) stderr = %q, want nothing", args, stderr)
		}
		if rec.calls() != 0 {
			t.Errorf("Run(%q) reached the backend, want none", args)
		}
	}
}

// TestUsageErrorsExitTwo: argv the command cannot act on is a usage error, it
// says which word was wrong, and it points at the help. Nothing touches a
// backend, and nothing touches the filesystem.
func TestUsageErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"--bogus"}, want: "--bogus"},
		{name: "unknown command", args: []string{"attach"}, want: "unknown command"},
		{name: "missing value", args: []string{"--session"}, want: "needs a value"},
		{name: "extra argument", args: []string{"run", "extra"}, want: "unexpected argument"},
		{name: "agent args on status", args: []string{"status", "--", "x"}, want: "no agent arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			rec := &recorder{}
			stdout, stderr, code := e.run(t, rec, tc.args...)
			if code != ExitUsage {
				t.Fatalf("exit = %d, want 2 (stderr: %s)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing on a usage error", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tc.want)
			}
			if !strings.Contains(stderr, "golder workspace --help") {
				t.Errorf("stderr = %q, want it to point at the help", stderr)
			}
			if rec.calls() != 0 {
				t.Error("a usage error reached the backend")
			}
			if _, err := os.Stat(filepath.Join(e.home, workspaceDirName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a usage error created %s", filepath.Join(e.home, workspaceDirName))
			}
		})
	}
}

// TestBackendErrorsExitOne: a backend failure is a failure and its message is
// the one the person reads, under the command's own name.
func TestBackendErrorsExitOne(t *testing.T) {
	boom := errors.New("the daemon went away")
	cases := []struct {
		name string
		rec  *recorder
		args []string
		want string
	}{
		{
			name: "start",
			rec:  &recorder{start: func(context.Context, Options) (StartResult, error) { return StartResult{}, boom }},
			want: "golder workspace: the daemon went away",
		},
		{
			name: "status",
			rec:  &recorder{status: func(context.Context, Options) ([]byte, error) { return nil, boom }},
			args: []string{"status"},
			want: "golder workspace status: the daemon went away",
		},
		{
			name: "stop",
			rec:  &recorder{stop: func(context.Context, Options) error { return boom }},
			args: []string{"stop"},
			want: "golder workspace stop: the daemon went away",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			stdout, stderr, code := e.run(t, tc.rec, tc.args...)
			if code != ExitFailure {
				t.Fatalf("exit = %d, want 1", code)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing on a failure", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
		})
	}
}

// TestCancelledContextNeverReachesTheBackend: an interrupted invocation does
// not start a daemon it would only have to stop, and it does not create the
// directories a start would need.
func TestCancelledContextNeverReachesTheBackend(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stdout, stderr, code := e.runCtx(t, ctx, rec)
	if code != ExitInterrupted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitInterrupted, stderr)
	}
	if rec.calls() != 0 {
		t.Error("a cancelled context reached the backend")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "interrupted") {
		t.Errorf("stderr = %q, want it to say the run was interrupted", stderr)
	}
	if _, err := os.Stat(filepath.Join(e.home, workspaceDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a cancelled run created %s", filepath.Join(e.home, workspaceDirName))
	}
}

// TestCancellationDuringTheBackendIsAnInterrupt: a backend that returns because
// its context ended did not fail. The exit code says "interrupted", not
// "broken".
func TestCancellationDuringTheBackendIsAnInterrupt(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &recorder{start: func(c context.Context, _ Options) (StartResult, error) {
		cancel()
		return StartResult{}, errors.New("attach aborted")
	}}

	_, stderr, code := e.runCtx(t, ctx, rec)
	if code != ExitInterrupted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, ExitInterrupted, stderr)
	}
	if !strings.Contains(stderr, "interrupted") {
		t.Errorf("stderr = %q, want it to say the run was interrupted", stderr)
	}
}

// TestNilBackendSaysSo: a build with no workspace wired in fails with a
// sentence rather than a nil dereference.
func TestNilBackendSaysSo(t *testing.T) {
	e := newTestEnv(t)
	for _, args := range [][]string{nil, {"status"}, {"stop"}} {
		_, stderr, code := e.run(t, nil, args...)
		if code != ExitFailure {
			t.Fatalf("Run(%q) exit = %d, want 1", args, code)
		}
		if !strings.Contains(stderr, "no workspace backend") {
			t.Errorf("Run(%q) stderr = %q, want it to name the missing backend", args, stderr)
		}
	}
}

// TestStartCreatesPrivateDirectories: the directories a workspace writes into
// are created by the command, 0700, and tightened if they already exist wider —
// they hold a session store, a trust table and a log of what the agent did.
func TestStartCreatesPrivateDirectories(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	if _, stderr, code := e.run(t, rec); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	got := rec.started[0]
	for _, dir := range []string{got.ProjectDir, got.StateDir, got.SocketDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("%s was not created: %v", dir, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s is mode %04o, want 0700", dir, perm)
		}
	}
	if _, err := os.Stat(filepath.Dir(got.LogFile)); err != nil {
		t.Errorf("the log's directory was not created: %v", err)
	}
}

// TestStartTightensAnExistingDirectory: a directory left 0755 by an older umask
// is tightened rather than refused, because it is golder's own.
func TestStartTightensAnExistingDirectory(t *testing.T) {
	e := newTestEnv(t)
	// Learn the paths from a first resolve, then widen the directory by hand.
	rec := &recorder{}
	if _, _, code := e.run(t, rec); code != ExitOK {
		t.Fatal("first start failed")
	}
	dir := rec.started[0].StateDir
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec = &recorder{}
	if _, stderr, code := e.run(t, rec); code != ExitOK {
		t.Fatalf("second start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("%s is mode %04o after a start, want 0700", dir, perm)
	}
}

// TestStatusAndStopCreateNothing: a command that only reads or only stops must
// not leave a directory tree behind on a machine that has never started a
// workspace.
func TestStatusAndStopCreateNothing(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"stop"}} {
		t.Run(args[0], func(t *testing.T) {
			e := newTestEnv(t)
			if _, stderr, code := e.run(t, &recorder{}, args...); code != ExitOK {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(e.home, workspaceDirName)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s created the workspace directory tree", args[0])
			}
			if _, err := os.Stat(RuntimeDir(e.deps().Getenv)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s created the runtime directory", args[0])
			}
		})
	}
}

// TestNothingIsWrittenIntoTheProject: a workspace keeps its files under the
// state root, never in the repository the person is working in. A .golder
// directory appearing in a clean checkout would be a surprise in every git
// status for the rest of the project's life.
func TestNothingIsWrittenIntoTheProject(t *testing.T) {
	e := newTestEnv(t)
	if _, stderr, code := e.run(t, &recorder{}, "--headless"); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	entries, err := os.ReadDir(e.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("the project directory gained %v, want it untouched", names)
	}
}

// TestRunDefaultSeamsNeedNoEnvironment: Run (the four-argument API) is wired to
// the real environment, so it is only asked what it can answer without one —
// that a cancelled context stops it before it reads anything.
func TestRunDefaultSeams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := Run(ctx, nil, &out, &errOut, &recorder{}); code != ExitInterrupted {
		t.Fatalf("exit = %d, want %d", code, ExitInterrupted)
	}
	// A nil stdout and stderr are discarded rather than panicking on write.
	if code := Run(ctx, nil, nil, nil, &recorder{}); code != ExitInterrupted {
		t.Fatalf("exit with nil writers = %d, want %d", code, ExitInterrupted)
	}
}

// errWriter fails every write, the way a closed pipe or a full disk does.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// TestOutputFailuresAreReported: output that cannot be written is a failure,
// not a silent success — a script that reads the readiness document must not
// get an empty one and a zero exit. `stop` is not in this list: its ack is
// informational, and the workspace is stopped whether or not the line reaches a
// screen (see runStop).
func TestOutputFailuresAreReported(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "readiness", args: []string{"--headless"}},
		{name: "status", args: []string{"status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			var errOut bytes.Buffer
			if code := RunWith(context.Background(), tc.args, errWriter{}, &errOut, &recorder{}, e.deps()); code != ExitFailure {
				t.Fatalf("exit = %d, want 1 (stderr: %s)", code, errOut.String())
			}
			if errOut.Len() == 0 {
				t.Error("a failed write said nothing")
			}
		})
	}
}

// TestUnusableStateRootIsAFailure: a state root that cannot be created is a
// failure with the path in it, and the backend is never asked to start a
// workspace whose state has nowhere to go.
func TestUnusableStateRootIsAFailure(t *testing.T) {
	e := newTestEnv(t)
	blocker := filepath.Join(e.base, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.set(golderhome.EnvHome, filepath.Join(blocker, "state"))

	rec := &recorder{}
	_, stderr, code := e.run(t, rec)
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1", code)
	}
	if rec.calls() != 0 {
		t.Error("a start with nowhere to keep its state reached the backend")
	}
	if !strings.Contains(stderr, "create") {
		t.Errorf("stderr = %q, want it to say what could not be created", stderr)
	}
}
