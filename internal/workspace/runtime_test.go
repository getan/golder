//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini/control"

	"github.com/getan/golder/internal/cli/workspacecmd"
)

func runtimeOptions(t *testing.T) workspacecmd.Options {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	runtime, err := os.MkdirTemp("/tmp", "gws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	session := filepath.Join(root, "sessions", "work")
	return workspacecmd.Options{
		Cwd: root, Session: "work", Socket: filepath.Join(runtime, "d.sock"), Headless: true, Shell: true,
		Paths: workspacecmd.Paths{
			Home: root, Project: root, ProjectDir: root, SessionDir: session,
			ConfigPath: filepath.Join(root, "config.toml"), StateDir: filepath.Join(session, "state"),
			LogFile: filepath.Join(session, "workspace.log"), SocketDir: runtime,
		},
	}
}

func TestBackendHeadlessStatusAndAuthenticatedStop(t *testing.T) {
	opts := runtimeOptions(t)
	backend := &Backend{Version: "integration-test"}
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := backend.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Socket != opts.Socket || res.Session != opts.Session {
		t.Fatalf("readiness = %+v", res)
	}
	record, err := readOwner(opts)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := backend.Status(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
		Panes   struct {
			Total int `json:"total"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(doc, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.PID != os.Getpid() || status.Panes.Total != 1 {
		t.Fatalf("status = %s", doc)
	}
	if strings.Contains(string(doc), record.Token) {
		t.Fatal("status exposed its private lifecycle proof")
	}
	wrong := record
	wrong.Token = strings.Repeat("0", len(record.Token))
	if _, err := callOwner(ctx, wrong, "stop"); err == nil {
		t.Fatal("an invalid proof stopped the owner")
	}
	if _, err := backend.Status(ctx, opts); err != nil {
		t.Fatalf("refused proof damaged the live owner: %v", err)
	}
	if err := (&Backend{}).Stop(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if err := backend.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	_, file := ownerPaths(opts)
	for _, path := range []string{opts.Socket, file} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cleanup left %s: %v", path, err)
		}
	}
	if err := backend.Stop(ctx, opts); err != nil {
		t.Fatalf("a second stop was not idempotent: %v", err)
	}
}

func TestStateLeaseStopsDifferentSocketsSharingState(t *testing.T) {
	first := runtimeOptions(t)
	second := runtimeOptions(t)
	second.StateDir = first.StateDir
	a, b := &Backend{}, &Backend{}
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	if _, err := a.Start(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Start(context.Background(), second); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a second owner of the state was accepted: %v", err)
	}
	if _, err := a.Status(context.Background(), first); err != nil {
		t.Fatalf("the loser damaged the first workspace: %v", err)
	}
	a.Close()
	if _, err := b.Start(context.Background(), second); err != nil {
		t.Fatalf("state lease was not released: %v", err)
	}
}

func TestBackendDoesNotAdoptAnUnrecordedSocket(t *testing.T) {
	opts := runtimeOptions(t)
	if err := os.WriteFile(opts.Socket, []byte("not our daemon"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{}
	if _, err := backend.Status(context.Background(), opts); err == nil {
		t.Fatal("status adopted an unowned socket")
	}
	if err := backend.Stop(context.Background(), opts); err == nil {
		t.Fatal("stop adopted an unowned socket")
	}
	data, err := os.ReadFile(opts.Socket)
	if err != nil || string(data) != "not our daemon" {
		t.Fatal("an unrelated path was changed")
	}
}

func TestBackendCancellationAndNonTTYLeaveNoDaemon(t *testing.T) {
	opts := runtimeOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Backend{}).Start(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start = %v", err)
	}
	pipe, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	opts.Headless = false
	if _, err := (&Backend{Input: pipe, Output: pipe}).Start(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("non-terminal start = %v", err)
	}
	if _, err := os.Stat(opts.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused start left a daemon socket")
	}
}

func TestInitialAgentArgumentsAreNeverShellText(t *testing.T) {
	opts := workspacecmd.Options{AgentArgs: []string{"--model", "a model", "--", "echo x; touch /must-not-exist"}}
	backend := &Backend{Executable: "/a path/golder"}
	got, err := backend.initialCommand(opts)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{backend.Executable}, opts.AgentArgs...)
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDeletingLastSessionStopsTheWorkspaceOwner(t *testing.T) {
	opts := runtimeOptions(t)
	backend := &Backend{}
	t.Cleanup(backend.Close)
	if _, err := backend.Start(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	client, err := control.Dial(context.Background(), opts.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Shutdown can close this connection before its reply is written. The
	// irreversible delete is not replayed; the owner's Done is the evidence.
	_, _ = client.Call(context.Background(), "kill-session", map[string]any{"session": opts.Session})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := backend.Wait(ctx); err != nil {
		t.Fatalf("empty daemon was left running: %v", err)
	}
}

func TestCustomSocketParentIsNeverChmodded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketParent(workspacecmd.Options{Socket: filepath.Join(dir, "d.sock")}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatal("a user-managed socket parent had its permissions changed")
	}
}
