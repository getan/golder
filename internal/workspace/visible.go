package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	workspaceui "github.com/getan/tuios-mini/workspace"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/diagtrace"
)

// A visible client is not the lifetime of its panes. Start the daemon using the
// same executable in headless mode, so leaving or detaching the UI keeps the
// agent, shell and browser alive. An explicit `workspace stop` ends the owner.
func (b *Backend) startVisible(ctx context.Context, opts workspacecmd.Options) (result workspacecmd.StartResult, runErr error) {
	in, out := b.Input, b.Output
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return workspacecmd.StartResult{}, errors.New("workspace: the visible workspace needs terminal input and output; use --headless for a daemon without a display")
	}
	if name := strings.TrimSpace(os.Getenv("TERM")); name == "" || name == "dumb" {
		return workspacecmd.StartResult{}, errors.New("workspace: TERM must describe a terminal capable of the visible workspace; use --headless otherwise")
	}
	if w, h, err := term.GetSize(out.Fd()); err == nil && (w < 40 || h < 12) {
		return workspacecmd.StartResult{}, fmt.Errorf("workspace: terminal is %dx%d; at least 40x12 is required", w, h)
	}
	b.mu.Lock()
	if b.server != nil || b.visible {
		b.mu.Unlock()
		return workspacecmd.StartResult{}, errors.New("workspace: this backend is already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	b.visible, b.cancel, b.done = true, cancel, done
	b.mu.Unlock()
	defer close(done)
	defer cancel()

	started := false
	var record ownerRecord
	defer func() {
		if started && runErr != nil && !errors.Is(runErr, context.Canceled) && record.Token != "" {
			stopNewOwner(record)
		}
	}()
	if opts.ManifestPath != "" {
		var err error
		opts, err = workspacecmd.ReloadSession(opts)
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	if opts.Command == workspacecmd.CommandNew && opts.Exists {
		return workspacecmd.StartResult{}, nameExists(opts.Session)
	}
	probeDone := diagtrace.Span("workspace", "owner.probe")
	record, _, running, err := probeOwner(runCtx, opts)
	probeDone()
	if err != nil {
		return workspacecmd.StartResult{}, err
	}
	if !running {
		if opts.Command == workspacecmd.CommandAttach || opts.Legacy {
			return workspacecmd.StartResult{}, fmt.Errorf("workspace: session %q is not running; attach never creates a session", opts.Session)
		}
		spawnDone := diagtrace.Span("workspace", "owner.spawn")
		res, err := b.spawnOwner(runCtx, opts)
		spawnDone()
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
		if opts.ManifestPath != "" {
			opts, err = workspacecmd.ReloadSession(opts)
			if err != nil {
				return workspacecmd.StartResult{}, err
			}
		}
		record, err = readOwner(opts)
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
		if res.Instance != record.Instance {
			return workspacecmd.StartResult{}, errors.New("workspace: daemon readiness and owner name different instances")
		}
		started = res.Created
		if _, err := callOwner(runCtx, record, "status"); err != nil {
			return workspacecmd.StartResult{}, err
		}
	} else {
		if opts.Command == workspacecmd.CommandNew {
			return workspacecmd.StartResult{}, nameExists(opts.Session)
		}
		if err := rejectStartOverrides(opts); err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	if err := runCtx.Err(); err != nil {
		if started {
			stopNewOwner(record)
		}
		return workspacecmd.StartResult{}, err
	}

	defer observeWorkspaceInput(diagtrace.Process())()
	defer traceWorkspaceStall(diagtrace.Process())()
	clientDone := diagtrace.Span("workspace", "client.run")
	err = workspaceui.Run(runCtx, workspaceui.Options{
		Socket: opts.Socket, Session: opts.Session, Version: b.version(),
		ConfigPath: record.ConfigPath, StateDir: filepath.Join(record.StateDir, "client"),
		Cwd: record.Project, Input: in, Output: out,
		DisableSignalHandling: true, StartInTerminalMode: true,
		ASCIIOnly: opts.Appearance.ASCIIOnly, NoAnimations: opts.Appearance.NoAnimations,
		Theme: opts.Appearance.Theme,
	})
	clientDone()
	var ended *workspaceui.ExitError
	if errors.As(err, &ended) && ended.Reason == "session-killed" {
		err = nil // deleting the last session intentionally ends this workspace
	} else if errors.As(err, &ended) && ended.Reason == "daemon-lost" {
		// A deliberate stop is normal; an unclean crash leaves its ownership
		// record behind and remains an error instead of being called a detach.
		current, ownerErr := readOwner(opts)
		if errors.Is(ownerErr, os.ErrNotExist) || ownerErr == nil && current.StopRequested {
			err = nil
		}
	}
	return workspacecmd.StartResult{Socket: opts.Socket, Session: opts.Session}, err
}

func stopNewOwner(record ownerRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Use the captured proof, not a later record at the same path: a failed UI
	// may clean up only the daemon it just started, never a replacement run.
	_, _ = callOwner(ctx, record, "stop")
}

func (b *Backend) spawnOwner(ctx context.Context, opts workspacecmd.Options) (workspacecmd.StartResult, error) {
	executable := b.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return workspacecmd.StartResult{}, err
	}
	if st, err := os.Stat(opts.Cwd); err != nil || !st.IsDir() {
		return workspacecmd.StartResult{}, fmt.Errorf("workspace: saved start directory %q is unavailable; use -C/--cwd", opts.Cwd)
	}
	if opts.ManifestPath != "" {
		if err := workspacecmd.EnsureIdentityDirs(opts); err != nil {
			return workspacecmd.StartResult{}, err
		}
	}
	command := "run"
	if opts.Command == workspacecmd.CommandNew {
		command = "new"
	}
	args := []string{"workspace", command, "--headless", "--session", opts.Session}
	// Do not turn the parent's defaults into explicit overrides. A concurrent
	// default run is allowed to attach to the winner without pretending it
	// requested a different start directory or initial command.
	if opts.CwdExplicit {
		args = append(args, "--cwd", opts.Cwd)
	}
	if opts.SocketExplicit {
		args = append(args, "--socket", opts.Socket)
	}
	if opts.ConfigExplicit {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.StateDirExplicit {
		args = append(args, "--state-dir", opts.StateDir)
	}
	if opts.Nested {
		args = append(args, "--nested")
	}
	if opts.Shell {
		args = append(args, "--shell")
	} else if len(opts.AgentArgs) > 0 {
		args = append(append(args, "--"), opts.AgentArgs...)
	}
	logFile, err := openLog(opts.LogFile)
	if err != nil {
		return workspacecmd.StartResult{}, err
	}
	defer logFile.Close()
	// Not CommandContext: after readiness, cancelling a UI must leave the
	// daemon alone. Only a failed startup may terminate this exact child.
	cmd := exec.Command(executable, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = withHome(os.Environ(), opts.Home)
	cmd.Stderr = logFile
	detachCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return workspacecmd.StartResult{}, err
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return workspacecmd.StartResult{}, fmt.Errorf("workspace: start own daemon: %w", err)
	}
	ready := make(chan error, 1)
	var result workspacecmd.StartResult
	go func() {
		err := json.NewDecoder(io.LimitReader(stdout, 64<<10)).Decode(&result)
		if err == nil && (result.Session != opts.Session || result.Socket == "" || result.Instance == "") {
			err = errors.New("daemon readiness named a different session or no instance")
		}
		ready <- err
	}()
	budget := time.NewTimer(15 * time.Second)
	defer budget.Stop()
	var startupErr error
	select {
	case err := <-ready:
		startupErr = err
	case <-ctx.Done():
		startupErr = ctx.Err()
	case <-budget.C:
		startupErr = errors.New("daemon startup did not become ready within 15s")
	}
	if startupErr != nil {
		_ = cmd.Process.Kill() // the exact child just started, never a pid-file target
		_ = stdout.Close()
		go func() { _ = cmd.Wait() }()
		return workspacecmd.StartResult{}, fmt.Errorf("workspace: %w; diagnostics: %s", startupErr, opts.LogFile)
	}
	// Wait closes StdoutPipe, so it must not race the readiness read. Reap the
	// child only after that read; a detached daemon is adopted if the UI exits.
	_ = stdout.Close()
	go func() { _ = cmd.Wait() }()
	return result, nil
}

func withHome(env []string, home string) []string {
	if home == "" {
		return env
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GOLDER_HOME=") {
			result = append(result, entry)
		}
	}
	return append(result, "GOLDER_HOME="+home)
}
