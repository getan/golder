//go:build darwin || linux

package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/getan/golder/internal/terminalstate"
)

type terminalLifecycleModel struct{}

func (terminalLifecycleModel) Init() tea.Cmd                         { return tea.Quit }
func (m terminalLifecycleModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (terminalLifecycleModel) View() tea.View {
	return tea.View{Content: "terminal lifecycle fixture", AltScreen: true, MouseMode: tea.MouseModeCellMotion}
}

func TestTerminalLifecycleFDPressureChild(t *testing.T) {
	if os.Getenv("GOLDER_TEST_TERMINAL_FD_PRESSURE") != "1" {
		return
	}
	before, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	// Darwin sets PENDIN when switching back to canonical input. Normalize
	// that kernel-maintained flag before comparing a later raw/restore cycle.
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		t.Fatal(err)
	}
	if err := term.Restore(os.Stdin.Fd(), before); err != nil {
		t.Fatal(err)
	}
	before, err = term.GetState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	// Signal setup may itself allocate a runtime pipe on macOS. Exclude that
	// separate fatal-runtime path to isolate the recoverable reader failure.
	p := tea.NewProgram(terminalLifecycleModel{}, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout), tea.WithoutSignalHandler())
	guard, err := terminalstate.Capture(os.Stdin, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	// Affect this disposable child only. Existing PTY fds and ioctls still
	// work, but the cancel reader cannot allocate its epoll/kqueue/pipe fds.
	limited := original
	limited.Cur = 0
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limited); err != nil {
		t.Fatal(err)
	}
	_, runErr := p.Run()
	_ = unix.Setrlimit(unix.RLIMIT_NOFILE, &original)
	restoreErr := guard.Restore()
	after, stateErr := term.GetState(os.Stdin.Fd())
	clean := restoreErr == nil && stateErr == nil && reflect.DeepEqual(before, after)
	// Restore the fixture even on the negative control; never leave the test
	// terminal raw merely to demonstrate the upstream failure.
	_ = term.Restore(os.Stdin.Fd(), before)
	fmt.Fprintf(os.Stderr, "\r\nFD_PRESSURE run_failed=%t terminal_restored=%t\r\n", runErr != nil, clean)
	if runErr == nil || !clean {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestTerminalLifecycleRestoresAfterInitFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTerminalLifecycleFDPressureChild$")
	cmd.Env = append(os.Environ(), "GOLDER_TEST_TERMINAL_FD_PRESSURE=1", "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	defer terminal.Close()
	defer cmd.Process.Kill()
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(terminal, 1<<16))
		output <- string(data)
	}()
	err = cmd.Wait()
	text := <-output
	if err != nil || !strings.Contains(text, "run_failed=true terminal_restored=true") {
		t.Fatalf("failed startup leaked terminal state: %v\n%s", err, text)
	}
}
