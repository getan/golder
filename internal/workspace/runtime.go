// Package workspace owns golder's integrated terminal workspace. The visible
// client and the persistent daemon use the same executable, in separate
// processes, so detaching a client does not kill its panes. The retained client
// and daemon are Go dependencies, not programs that must be installed on PATH.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/getan/tuios-mini/mini"
	"github.com/getan/tuios-mini/mini/control"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/privatepath"
)

// Backend implements workspacecmd.Backend. One Backend owns at most one
// workspace; Close must be called even if printing readiness fails.
type Backend struct {
	Version    string
	Executable string
	Input      *os.File
	Output     *os.File

	mu      sync.Mutex
	server  *mini.Server
	owner   *ownerServer
	lease   *stateLease
	cancel  context.CancelFunc
	done    chan struct{}
	visible bool
}

// Start starts the daemon and its first pane, then runs the visible client
// unless Headless was requested. In headless mode the caller prints readiness
// and calls Wait; returning from Start must not terminate a ready daemon.
func (b *Backend) Start(ctx context.Context, opts workspacecmd.Options) (workspacecmd.StartResult, error) {
	if err := supported(); err != nil {
		return workspacecmd.StartResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return workspacecmd.StartResult{}, err
	}
	if opts.Legacy && opts.Command != workspacecmd.CommandAttach {
		return workspacecmd.StartResult{}, errors.New("workspace: legacy sessions support attach, status and stop only")
	}
	if !opts.Headless {
		return b.startVisible(ctx, opts)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != nil || b.visible {
		return workspacecmd.StartResult{}, errors.New("workspace: this backend already owns a daemon")
	}
	if opts.Command == workspacecmd.CommandAttach {
		return workspacecmd.StartResult{}, errors.New("workspace: attach requires a visible client")
	}
	var nameLease *stateLease
	if opts.ManifestPath != "" {
		var running bool
		var err error
		opts, nameLease, running, err = reserveStart(ctx, opts)
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
		if running {
			r, err := readOwner(opts)
			return ownerResult(r, false), err
		}
		// The name lease is transferred to the daemon's cleanup goroutine only
		// after all startup steps, including publication of session.json, succeed.
		defer func() { nameLease.Close() }()
		if _, _, running, err := probeOwner(ctx, opts); err != nil {
			return workspacecmd.StartResult{}, err
		} else if running {
			return workspacecmd.StartResult{}, errors.New("workspace: a live owner exists without its session lease; refusing to replace it")
		}
		if err := workspacecmd.EnsureIdentityDirs(opts); err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	if st, err := os.Stat(opts.Cwd); err != nil || !st.IsDir() {
		return workspacecmd.StartResult{}, fmt.Errorf("workspace: saved start directory %q is unavailable; use -C/--cwd to select a directory", opts.Cwd)
	}
	out := b.Output
	if out == nil {
		out = os.Stdout
	}
	for _, dir := range []string{opts.SessionDir, opts.StateDir, filepath.Dir(opts.LogFile)} {
		if err := privatepath.EnsureDir(dir); err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	// A custom config/socket parent can be the project directory or /tmp.
	// Never chmod those directories as though they belonged to the workspace.
	if err := os.MkdirAll(filepath.Dir(opts.ConfigPath), 0o700); err != nil {
		return workspacecmd.StartResult{}, err
	}
	if err := ensureSocketParent(opts); err != nil {
		return workspacecmd.StartResult{}, err
	}
	lease, err := lockState(opts.StateDir)
	if err != nil {
		return workspacecmd.StartResult{}, err
	}
	command, err := b.initialCommand(opts)
	if err != nil {
		lease.Close()
		return workspacecmd.StartResult{}, err
	}
	harnessDir, err := b.writeHarness(opts.StateDir)
	if err != nil {
		lease.Close()
		return workspacecmd.StartResult{}, err
	}
	width, height := 100, 30
	if w, h, err := term.GetSize(out.Fd()); err == nil && w > 0 && h > 0 {
		width, height = w, h
	}
	runCtx, cancel := context.WithCancel(ctx)
	paneEnv := []string{"GOLDER_WORKSPACE=1"}
	// A named session may contain panes in many projects. Never advertise its
	// initial cwd as a daemon-wide project or use it to widen agent permissions.
	if opts.ManifestPath == "" {
		paneEnv = append(paneEnv, "GOLDER_PROJECT="+opts.Cwd)
	} else {
		paneEnv = append(paneEnv, "GOLDER_PROJECT=")
	}
	if opts.Home != "" {
		paneEnv = append(paneEnv, "GOLDER_HOME="+opts.Home)
	}
	srv, err := mini.Start(runCtx, mini.Config{
		SocketPath: opts.Socket, Session: opts.Session, Cwd: opts.Cwd,
		StateDir: opts.StateDir, LogFile: opts.LogFile, Version: b.version(),
		Width: width, Height: height, Command: command,
		PaneEnv:         paneEnv,
		PermissionsMode: "strict", Grants: []string{"read", "write", "fan"},
		StopWhenEmpty: true,
		HarnessDir:    harnessDir,
	})
	if err != nil {
		cancel()
		lease.Close()
		return workspacecmd.StartResult{}, err
	}
	owner, err := startOwner(runCtx, opts, b.version(), cancel)
	if err != nil {
		cancel()
		srv.Stop()
		lease.Close()
		return workspacecmd.StartResult{}, err
	}
	if opts.ManifestPath != "" {
		if err := workspacecmd.SaveSession(opts); err != nil {
			cancel()
			srv.Stop()
			owner.Close()
			lease.Close()
			return workspacecmd.StartResult{}, err
		}
	}
	b.server, b.owner, b.lease, b.cancel = srv, owner, lease, cancel
	b.done = make(chan struct{})
	heldNameLease := nameLease
	nameLease = nil
	go func() {
		<-srv.Done()
		owner.Close()
		lease.Close()
		heldNameLease.Close()
		close(b.done)
	}()
	result := ownerResult(owner.record, true)
	return result, nil
}

func ownerResult(r ownerRecord, created bool) workspacecmd.StartResult {
	return workspacecmd.StartResult{
		Session: r.Session, Socket: r.Socket, Cwd: r.Project,
		Config: r.ConfigPath, StateDir: r.StateDir, Log: r.LogFile,
		Created: created, Instance: r.Instance,
	}
}

func (b *Backend) version() string {
	if b.Version != "" {
		return b.Version
	}
	return "golder-dev"
}

func (b *Backend) initialCommand(opts workspacecmd.Options) ([]string, error) {
	if opts.Shell {
		return nil, nil
	}
	executable := b.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("workspace: locate golder executable: %w", err)
		}
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve golder executable: %w", err)
	}
	return append([]string{abs}, opts.AgentArgs...), nil
}

// Wait keeps a headless workspace alive after readiness has been written. A
// normal owner-stop request succeeds; cancellation returns the caller's error.
func (b *Backend) Wait(ctx context.Context) error {
	b.mu.Lock()
	done := b.done
	b.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		b.Close()
		return ctx.Err()
	}
}

// Close stops only this backend's daemon, releases its state lease and waits
// for cleanup. It never signals a pid read from a file or kills another server.
func (b *Backend) Close() {
	b.mu.Lock()
	cancel, srv, done := b.cancel, b.server, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		srv.Stop()
	}
	if done != nil {
		<-done
	}
}

// Status proves ownership through the private lifecycle endpoint, then asks
// the ordinary control protocol for this session's panes. No executable names,
// stale pid files or focused panes are used as identity evidence.
func (b *Backend) Status(ctx context.Context, opts workspacecmd.Options) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, info, running, err := probeOwner(ctx, opts)
	if err != nil {
		return nil, err
	}
	if !running {
		if err := requireIdle(opts); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"running": false, "session": opts.Session, "socket": opts.Socket, "cwd": opts.Cwd, "legacy": opts.Legacy})
	}
	client, err := control.Dial(ctx, opts.Socket)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	windows, err := client.Call(ctx, "list-windows", map[string]any{"session": opts.Session})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"running": true, "pid": info.PID, "version": info.Version, "instance": info.Instance,
		"session": opts.Session, "socket": opts.Socket, "cwd": record.Project, "legacy": opts.Legacy,
		"config": record.ConfigPath, "state_dir": record.StateDir, "log": record.LogFile,
		"panes": json.RawMessage(windows),
	})
}

// Stop asks the exact workspace owner to cancel its own context. The token is
// read from an owner-only record, never returned in status/readiness or logged.
func (b *Backend) Stop(ctx context.Context, opts workspacecmd.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	record, _, running, err := probeOwner(ctx, opts)
	if err != nil {
		return err
	}
	if !running {
		return requireIdle(opts)
	}
	if _, err := callOwner(ctx, record, "stop"); err != nil {
		return err
	}
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	budget := time.NewTimer(10 * time.Second)
	defer budget.Stop()
	for {
		if _, err := os.Lstat(opts.Socket); errors.Is(err, os.ErrNotExist) {
			_, file := ownerPaths(opts)
			if _, err := os.Lstat(file); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		} else if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-budget.C:
			return errors.New("workspace: stop was accepted but daemon cleanup did not finish within 10s")
		case <-timer.C:
		}
	}
}

func ensureSocketParent(opts workspacecmd.Options) error {
	if opts.SocketDir != "" {
		return privatepath.EnsureDir(opts.SocketDir)
	}
	dir := filepath.Dir(opts.Socket)
	// A user-selected socket may use a system alias such as macOS's /tmp.
	// Inspect its target, but never create or chmod this user-managed parent.
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("workspace: socket directory %q is not a directory", dir)
	}
	if st.Mode().Perm()&0o022 != 0 && st.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("workspace: socket directory %q is writable by other users; choose a private directory", dir)
	}
	return nil
}
