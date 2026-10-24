//go:build darwin || linux

package workspace

// The first agent pane: what it runs, and what it is told about itself.
//
// Two things a person depends on and no unit test of the daemon would show:
// the arguments after `--` reach the agent as *arguments* (never joined into a
// shell line), and the pane's first process is given the identity of the pane
// it runs in — the workspace markers and the tuios variables golder's own tools
// and host reporting read — rather than inheriting the caller's environment.
//
// The backend is driven in-process so the executable it names at its first pane
// can be this test binary: a real process in a real pane, which writes down the
// argv it was run with and the environment it was given.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/workspacecmd"
)

// paneDump is what the pane's agent wrote down.
type paneDump struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Cwd  string   `json:"cwd"`
}

// TestFirstAgentPaneRunsTheLiteralArgvWithThePanesIdentity starts a workspace
// whose first pane is an agent, and reads back what that agent was run as.
func TestFirstAgentPaneRunsTheLiteralArgvWithThePanesIdentity(t *testing.T) {
	root := tempRoot(t)
	dir := project(t, root)
	socket := filepath.Join(root, "s.sock")
	projectDir := filepath.Join(root, "golder", "workspace", "project")
	sessionDir := filepath.Join(projectDir, "sessions", "agent")
	opts := workspacecmd.Options{
		Cwd:      dir,
		Session:  "agent",
		Socket:   socket,
		Headless: true,
		// An argv, not a command line: spaces, a semicolon that would start a
		// second shell command, and arguments that look like flags all stay
		// exactly where they were put.
		AgentArgs: []string{"--model", "a model", "--", "echo x; touch /must-not-exist"},
		Paths: workspacecmd.Paths{
			Home: filepath.Join(root, "golder"), Project: dir, ProjectID: "pad",
			ProjectDir: projectDir, SessionDir: sessionDir,
			ConfigPath: filepath.Join(projectDir, "config.toml"),
			StateDir:   filepath.Join(sessionDir, "state"),
			LogFile:    filepath.Join(sessionDir, "workspace.log"),
			SocketDir:  root,
		},
	}
	dumpPath := filepath.Join(root, "pane.json")
	// The pane runs this test binary, which recognises the variable in TestMain
	// and writes down its own argv and environment instead of running tests.
	t.Setenv(paneDumpEnv, dumpPath)
	t.Setenv("SHELL", "/bin/sh")

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Version: "e2e-agent", Executable: executable}
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := backend.Start(ctx, opts); err != nil {
		t.Fatalf("start the workspace: %v", err)
	}

	var dump paneDump
	waitFor(t, 20*time.Second, "the first pane to report what it was run as", func() bool {
		data, err := os.ReadFile(dumpPath)
		if err != nil || len(data) == 0 {
			return false
		}
		return json.Unmarshal(data, &dump) == nil
	})

	// The argv is the executable and the arguments exactly as they were given:
	// no shell, no re-splitting, no joining.
	want := append([]string{executable}, opts.AgentArgs...)
	if len(dump.Argv) != len(want) {
		t.Fatalf("the pane's argv is %q, want %q", dump.Argv, want)
	}
	for i := range want {
		if dump.Argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, dump.Argv[i], want[i])
		}
	}
	// Nothing interpreted the argument that looks like a command line: a shell
	// would have run `touch`, which would leave a file behind.
	for _, path := range []string{filepath.Join(dir, "touch"), "/must-not-exist"} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("a shell interpreted the agent's arguments: %s exists", path)
		}
	}
	// The pane starts in the project, not wherever the caller happened to be.
	if dump.Cwd != canonical(t, dir) {
		t.Errorf("the pane started in %q, want %q", dump.Cwd, dir)
	}

	// The identity: golder's own markers, then the pane contract the daemon
	// stamps. Every one of these is what the reporting and the pane tools read.
	paneEnv := environMap(dump.Env)
	for name, want := range map[string]string{
		"GOLDER_WORKSPACE": "1",
		"GOLDER_PROJECT":   canonical(t, dir),
		"GOLDER_HOME":      opts.Home,
		"TUIOS_ENV":        "1",
		"TUIOS_SOCKET":     socket,
		"TUIOS_SESSION":    "agent",
	} {
		if got := paneEnv[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if paneEnv["TUIOS_PANE_ID"] == "" {
		t.Error("the pane was started without TUIOS_PANE_ID")
	}
	// A token the daemon minted for this pane, not a name the caller chose: 128
	// bits of HMAC, hex.
	if token := paneEnv["TUIOS_PANE_TOKEN"]; len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Errorf("TUIOS_PANE_TOKEN = %q, want 32 hex characters", token)
	}
	for _, grant := range []string{"read", "write", "fan"} {
		if !strings.Contains(paneEnv["TUIOS_PANE_GRANTS"], grant) {
			t.Errorf("TUIOS_PANE_GRANTS = %q, want %q in it", paneEnv["TUIOS_PANE_GRANTS"], grant)
		}
	}
	// The pane is the pane the daemon lists, and it is the one this workspace
	// says it has.
	stdout, err := backend.Status(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	status := parseStatus(t, string(stdout))
	if status.Panes.Total != 1 {
		t.Fatalf("panes = %d, want the one agent pane", status.Panes.Total)
	}
	if got := status.Panes.Windows[0].WindowID; got != paneEnv["TUIOS_PANE_ID"] {
		t.Errorf("the pane's TUIOS_PANE_ID is %q, and the daemon lists %q", paneEnv["TUIOS_PANE_ID"], got)
	}
	// Nothing about the caller leaked in: no herdr contract, no reporting
	// override.
	for _, name := range []string{"HERDR_ENV", "HERDR_BIN_PATH", "GOLDER_HOST_REPORT"} {
		if _, ok := paneEnv[name]; ok {
			t.Errorf("the pane inherited %s from the caller", name)
		}
	}
}

// environMap reads an environment list the way the operating system resolves
// it: the first value of a duplicated name wins.
func environMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, seen := out[name]; seen {
			continue
		}
		out[name] = value
	}
	return out
}
