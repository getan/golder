package terminalaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/charmbracelet/x/xpty"
)

// Process owns a cross-platform PTY and its child. It is intentionally separate
// from the terminal emulator, so local and authorized Agent writes go through
// Connection instead of being queued independently by MyGo.
type Process struct {
	pty      xpty.Pty
	cmd      *exec.Cmd
	done     chan struct{}
	once     sync.Once
	closeErr error
}

type ProcessOptions struct {
	Argv    []string
	Dir     string
	Env     []string // nil inherits the user's environment
	Cleanup func()   // invoked after exit, including a failed start
}

// StartProcess starts exactly argv in a PTY; it does not authorize a command or
// create a sandbox. Agent callers must supply a previously authorized envelope.
func StartProcess(o ProcessOptions) (*Process, error) {
	started := false
	defer func() {
		if !started && o.Cleanup != nil {
			o.Cleanup()
		}
	}()
	if len(o.Argv) == 0 || strings.TrimSpace(o.Argv[0]) == "" {
		return nil, errors.New("terminal: empty program")
	}
	pty, err := xpty.NewPty(80, 24)
	if err != nil {
		return nil, fmt.Errorf("terminal: allocate PTY: %w", err)
	}
	cmd := exec.Command(o.Argv[0], o.Argv[1:]...)
	cmd.Dir = o.Dir
	base := o.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = terminalEnvironment(base)
	configureProcess(cmd)
	if err := pty.Start(cmd); err != nil {
		_ = pty.Close()
		return nil, fmt.Errorf("terminal: start program: %w", err)
	}
	closeParentSlave(pty)
	p := &Process{pty: pty, cmd: cmd, done: make(chan struct{})}
	started = true
	go func() {
		_ = xpty.WaitProcess(context.Background(), cmd)
		onProcessExit(pty)
		if o.Cleanup != nil {
			o.Cleanup()
		}
		close(p.done)
	}()
	return p, nil
}

func terminalEnvironment(base []string) []string {
	out := make([]string, 0, len(base)+2)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "TERM") || strings.EqualFold(name, "COLORTERM") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
}

// UserShell returns the login shell chosen by the person, as terminal apps do.
func UserShell() []string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			return []string{"powershell.exe"}
		}
		return []string{"cmd.exe"}
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/sh"
		}
	}
	return []string{shell, "-l"}
}

func (p *Process) Read(b []byte) (int, error) {
	n, err := p.pty.Read(b)
	if n == 0 && terminalEOF(err) {
		return 0, io.EOF
	}
	return n, err
}
func (p *Process) Write(b []byte) (int, error) { return p.pty.Write(b) }
func (p *Process) Resize(cols, rows int) error { return p.pty.Resize(cols, rows) }
func (p *Process) Done() <-chan struct{}       { return p.done }

// Close terminates the GUI-owned process and closes its PTY. Attaching to an
// existing bash TTY uses a different connection whose Close only detaches.
func (p *Process) Close() error {
	p.once.Do(func() {
		select {
		case <-p.done:
		default:
			killProcess(p.cmd)
		}
		p.closeErr = p.pty.Close()
		if errors.Is(p.closeErr, os.ErrClosed) {
			p.closeErr = nil // the parent slave was intentionally closed at start
		}
	})
	return p.closeErr
}
