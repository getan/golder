//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"golang.org/x/sys/unix"
)

func namedOptions(t *testing.T) workspacecmd.Options {
	t.Helper()
	o := runtimeOptions(t)
	o.Command = workspacecmd.CommandRun
	o.SessionDir = filepath.Join(o.Home, "workspace", "sessions", o.Session)
	o.ProjectDir = o.SessionDir
	o.ConfigPath = filepath.Join(o.SessionDir, "config.toml")
	o.StateDir = filepath.Join(o.SessionDir, "state")
	o.LogFile = filepath.Join(o.SessionDir, "workspace.log")
	o.ManifestPath = filepath.Join(o.SessionDir, "session.json")
	return o
}

func TestNamedDaemonLifecycleAndDirectoryIndependentOwner(t *testing.T) {
	o := namedOptions(t)
	a := &Backend{}
	t.Cleanup(a.Close)
	res, err := a.Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Instance == "" {
		t.Fatalf("new daemon = %+v", res)
	}
	for _, dir := range []string{o.SessionDir, o.StateDir, o.SocketDir} {
		st, err := os.Stat(dir)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("nonprivate/missing directory: %s, %v", dir, err)
		}
	}
	other := o
	other.Cwd = filepath.Join(o.Cwd, "a-directory-that-does-not-exist")
	other.Shell = false
	other, err = workspacecmd.ReloadSession(other)
	if err != nil {
		t.Fatal(err)
	}
	if other.Cwd != o.Cwd {
		t.Fatal("reload did not restore the saved start directory")
	}
	// readOwner for a named session is not tied to the caller's cwd.
	query := other
	query.Cwd = "/not-the-start-directory"
	if _, err := readOwner(query); err != nil {
		t.Fatalf("owner still binds to cwd: %v", err)
	}
	b := &Backend{}
	again, err := b.Start(context.Background(), other)
	if err != nil || again.Created || again.Instance != res.Instance {
		t.Fatalf("reuse = %+v, %v", again, err)
	}
	b.Close() // a client/reuse backend must not stop the daemon it found
	if _, err := a.Status(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*workspacecmd.Options){
		func(o *workspacecmd.Options) { o.CwdExplicit = true },
		func(o *workspacecmd.Options) { o.Shell = true },
		func(o *workspacecmd.Options) { o.AgentArgs = []string{"--model", "ignored"} },
	} {
		bad := other
		mutate(&bad)
		if _, err := (&Backend{}).Start(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("running start override accepted: %v", err)
		}
	}
	if err := (&Backend{}).Remove(context.Background(), other); err == nil {
		t.Fatal("rm removed a running session")
	}
	if err := (&Backend{}).Stop(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.ManifestPath); err != nil {
		t.Fatal("stop deleted the durable session record")
	}
	strict := other
	strict.Command = workspacecmd.CommandNew
	if _, err := (&Backend{}).Start(context.Background(), strict); err == nil {
		t.Fatal("new overwrote a stopped record")
	}
	lockBefore, _ := os.Stat(workspacecmd.SessionLockPath(other))
	if err := (&Backend{}).Remove(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	lockAfter, _ := os.Stat(workspacecmd.SessionLockPath(other))
	if lockBefore == nil || lockAfter == nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("rm unlinked the stable name-lock inode")
	}
	if _, err := os.Stat(o.SessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rm left the session directory")
	}
}

func TestConcurrentNamedStartsPublishOneDaemon(t *testing.T) {
	o := namedOptions(t)
	o.Shell = false
	const count = 5
	backends := make([]*Backend, count)
	results := make([]workspacecmd.StartResult, count)
	errs := make([]error, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := range count {
		backends[i] = &Backend{Executable: "/bin/sh"}
		t.Cleanup(backends[i].Close)
		wg.Go(func() {
			<-start
			results[i], errs[i] = backends[i].Start(ctx, o)
		})
	}
	close(start)
	wg.Wait()
	created := 0
	instance := ""
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if r.Created {
			created++
		}
		if instance == "" {
			instance = r.Instance
		}
		if instance != r.Instance {
			t.Fatal("same name acquired different daemon instances")
		}
	}
	if created != 1 {
		t.Fatalf("created %d daemons, want exactly one", created)
	}
}

func TestNamedRestartUpdatesCwdButNotCreationTime(t *testing.T) {
	o := namedOptions(t)
	o.CreatedAt = "2026-01-02T03:04:05Z"
	if err := workspacecmd.SaveSession(o); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restarted := o
	restarted.Cwd = t.TempDir()
	restarted.CwdExplicit = true
	b := &Backend{}
	t.Cleanup(b.Close)
	if _, err := b.Start(context.Background(), restarted); err != nil {
		t.Fatal(err)
	}
	loaded, err := workspacecmd.ReloadSession(o)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cwd != restarted.Cwd || loaded.CreatedAt != o.CreatedAt {
		t.Fatalf("restart metadata = %+v", loaded)
	}
	if err := (&Backend{}).Stop(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
}

func TestNamedRemovePreservesExternalOverridesAndRejectsSymlinks(t *testing.T) {
	o := namedOptions(t)
	o.StateDir = t.TempDir()
	marker := filepath.Join(o.StateDir, "keep")
	if err := os.WriteFile(marker, []byte("external state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspacecmd.SaveSession(o); err != nil {
		t.Fatal(err)
	}
	if err := (&Backend{}).Remove(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("rm deleted a custom state directory")
	}
	if err := os.Symlink(o.StateDir, o.SessionDir); err != nil {
		t.Skip(err)
	}
	if err := (&Backend{}).Remove(context.Background(), o); err == nil {
		t.Fatal("rm accepted a symlinked session directory")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("rm followed the directory link")
	}
}

func TestStatusAndStopDoNotCallAStartingLeaseStopped(t *testing.T) {
	o := namedOptions(t)
	lease, err := lockSession(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := (&Backend{}).Status(context.Background(), o); err == nil {
		t.Fatal("held lease was reported stopped")
	}
	if err := (&Backend{}).Stop(context.Background(), o); err == nil {
		t.Fatal("stop claimed success while a daemon was starting")
	}
	if _, err := os.Stat(o.ManifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe created a manifest")
	}
}

func TestTimeoutAndBadProofNeverAuthorizeRemoval(t *testing.T) {
	o := namedOptions(t)
	b := &Backend{}
	t.Cleanup(b.Close)
	if _, err := b.Start(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	record, err := readOwner(o)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	// A socket that accepts but never answers is unreachable, not stopped.
	ln, err := net.Listen("unix", record.Control)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, file := ownerPaths(o)
	if err := writeOwner(file, record); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := (&Backend{}).Status(ctx, o); err == nil {
		t.Fatal("silent owner was called stopped")
	}
	if err := (&Backend{}).Remove(ctx, o); err == nil {
		t.Fatal("an unreachable owner authorised rm")
	}
	if _, err := os.Stat(o.ManifestPath); err != nil {
		t.Fatal("unreachable session record was removed")
	}
}

func TestLegacyDaemonRemainsDiscoverableAndStoppable(t *testing.T) {
	o := runtimeOptions(t)
	// Build the actual old on-disk layout, with the old project-bound proof.
	o.ProjectID = "123456789abc"
	o.ProjectDir = filepath.Join(o.Home, "workspace", o.ProjectID)
	o.SessionDir = filepath.Join(o.ProjectDir, "sessions", o.Session)
	o.ConfigPath = filepath.Join(o.ProjectDir, "config.toml")
	o.StateDir = filepath.Join(o.SessionDir, "state")
	o.LogFile = filepath.Join(o.SessionDir, "workspace.log")
	b := &Backend{}
	t.Cleanup(b.Close)
	if _, err := b.Start(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	options, err := workspacecmd.Discover(o.Home)
	if err != nil || len(options) != 1 {
		t.Fatalf("discover legacy = %+v, %v", options, err)
	}
	legacy := options[0]
	if !legacy.Legacy || legacy.Socket != o.Socket || legacy.Cwd != o.Cwd {
		t.Fatalf("legacy target = %+v", legacy)
	}
	doc, err := (&Backend{}).Status(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Running bool `json:"running"`
	}
	if json.Unmarshal(doc, &status) != nil || !status.Running {
		t.Fatalf("legacy status = %s", doc)
	}
	if err := (&Backend{}).Remove(context.Background(), legacy); err == nil {
		t.Fatal("rm accepted legacy state")
	}
	if err := (&Backend{}).Stop(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
}

func TestReuseWaitsForDurableRecordPublication(t *testing.T) {
	o := namedOptions(t)
	o.Shell = false
	lease, err := lockSession(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The lifecycle endpoint can exist for an instant before SaveSession has
	// committed the catalog record. Reuse must wait for both, not publish an
	// incomplete readiness response or fall through without the name lease.
	owner, err := startOwner(ctx, o, "test", cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	type reply struct {
		result workspacecmd.StartResult
		err    error
	}
	done := make(chan reply, 1)
	go func() {
		res, err := (&Backend{}).Start(ctx, o)
		done <- reply{res, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("reuse returned before the manifest was published: %+v, %v", r.result, r.err)
	case <-time.After(60 * time.Millisecond):
	}
	if err := workspacecmd.SaveSession(o); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.result.Created || r.result.Instance != owner.record.Instance {
			t.Fatalf("reuse after publication = %+v, %v", r.result, r.err)
		}
	case <-ctx.Done():
		t.Fatal("reuse did not observe the published record")
	}
}

func TestLeaseAndLogRejectSpecialFilesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if lease, err := openLease(path, false); err == nil {
		lease.Close()
		t.Fatal("read-only lease probe accepted a FIFO")
	}
	if log, err := openLog(path); err == nil {
		log.Close()
		t.Fatal("log accepted a FIFO")
	}
	o := namedOptions(t)
	if err := workspacecmd.EnsureIdentityDirs(o); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(o.ManifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspacecmd.ReloadSession(o); err == nil {
		t.Fatal("session metadata accepted a FIFO")
	}
}
