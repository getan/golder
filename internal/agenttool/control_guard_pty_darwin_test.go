//go:build darwin

package agenttool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/seatbelt"
)

// Test binaries opt into the same bootstrap protocol as cmd/golder. Library
// callers otherwise cannot accidentally re-execute an arbitrary SDK host.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == seatbelt.TerminalBootstrapFlag {
		if err := seatbelt.ExecTerminalSandbox(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := seatbelt.ConfigureTerminalLauncher(os.Args[0]); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// This is the command envelope used by agent-created workspace/tuios panes,
// not an ordinary bare PTY and not BashTool's separate execution path.
func TestControlledPaneCanSetOwnTerminalEcho(t *testing.T) {
	if !seatbelt.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	g := NewControlGuard(t.TempDir())
	argv, cleanup, isolated, err := g.launchArgv(context.Background(), []string{
		"/bin/bash", "--noprofile", "--norc", "-c",
		`set -eu; original=$(stty -g); trap 'stty "$original"' EXIT; stty -echo; stty -a; stty echo; printf 'OWN_TERMIOS_OK\n'`,
	}, g.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !isolated {
		t.Fatal("terminal compatibility must not remove the pane sandbox")
	}
	m := execsess.NewManager()
	s, err := m.Start(execsess.StartRequest{Argv: argv, Dir: g.Root, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill()
	if !s.Wait(context.Background(), 5*time.Second, execsess.WaitExit) {
		t.Fatal("controlled pane failed to finish its termios round trip")
	}
	out := s.ReadNew()
	if strings.Contains(out, "sandbox_apply: Operation not permitted") {
		t.Skip("outer sandbox cannot apply a nested profile")
	}
	if s.Snapshot().ExitCode != 0 || !strings.Contains(out, "OWN_TERMIOS_OK") {
		t.Fatalf("controlled pane cannot control its own terminal: %q", out)
	}
}
