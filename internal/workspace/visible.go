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
	record, err := readOwner(opts)
	defer func() {
		if started && runErr != nil && !errors.Is(runErr, context.Canceled) && record.Token != "" {
			stopNewOwner(record)
		}
	}()
	if err == nil {
		_, err = callOwner(runCtx, record, "status")
	}
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !refusedSocket(err) {
			return workspacecmd.StartResult{}, fmt.Errorf("workspace: existing owner could not be verified: %w", err)
		}
		if err := b.spawnOwner(runCtx, opts); err != nil {
			return workspacecmd.StartResult{}, err
		}
		started = true
		record, err = readOwner(opts)
		if err != nil {
			return workspacecmd.StartResult{}, err
		}
		if _, err := callOwner(runCtx, record, "status"); err != nil {
			return workspacecmd.StartResult{}, err
		}
	} else if len(opts.AgentArgs) != 0 || opts.Shell {
		return workspacecmd.StartResult{}, errors.New("workspace: this session is already running; omit --shell and agent arguments to attach, or choose another --session")
	}
	if err := runCtx.Err(); err != nil {
		if started {
			stopNewOwner(record)
		}
		return workspacecmd.StartResult{}, err
	}

	err = workspaceui.Run(runCtx, workspaceui.Options{
		Socket: opts.Socket, Session: opts.Session, Version: b.version(),
		ConfigPath: record.ConfigPath, StateDir: filepath.Join(record.StateDir, "client"),
		Cwd: opts.Cwd, Input: in, Output: out,
		DisableSignalHandling: true, StartInTerminalMode: true,
		ASCIIOnly: opts.Appearance.ASCIIOnly, NoAnimations: opts.Appearance.NoAnimations,
		Theme: opts.Appearance.Theme,
	})
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

func (b *Backend) spawnOwner(ctx context.Context, opts workspacecmd.Options) error {
	executable := b.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	args := []string{
		"workspace", "run", "--headless", "--cwd", opts.Cwd, "--session", opts.Session,
		"--socket", opts.Socket, "--state-dir", opts.StateDir, "--config", opts.ConfigPath,
	}
	if opts.Nested {
		args = append(args, "--nested")
	}
	if opts.Shell {
		args = append(args, "--shell")
	} else if len(opts.AgentArgs) > 0 {
		args = append(append(args, "--"), opts.AgentArgs...)
	}
	logFile, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
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
		return err
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("workspace: start own daemon: %w", err)
	}
	ready := make(chan error, 1)
	go func() {
		var res workspacecmd.StartResult
		err := json.NewDecoder(io.LimitReader(stdout, 64<<10)).Decode(&res)
		if err == nil && (res.Socket != opts.Socket || res.Session != opts.Session) {
			err = errors.New("daemon readiness named a different socket or session")
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
		return fmt.Errorf("workspace: %w; diagnostics: %s", startupErr, opts.LogFile)
	}
	// Wait closes StdoutPipe, so it must not race the readiness read. Reap the
	// child only after that read; a detached daemon is adopted if the UI exits.
	_ = stdout.Close()
	go func() { _ = cmd.Wait() }()
	return nil
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
