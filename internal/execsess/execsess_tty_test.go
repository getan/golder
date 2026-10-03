//go:build !windows

package execsess

import (
	"context"
	"strings"
	"testing"
	"time"
)

func startTTY(t *testing.T, m *Manager, command string) *Session {
	t.Helper()
	s, err := m.Start(StartRequest{Command: command, Argv: bashArgv(command), TTY: true})
	if err != nil {
		t.Fatalf("Start tty: %v", err)
	}
	return s
}

// The child must see a 24x80 terminal, not a pipe.
func TestTTYWindowSize(t *testing.T) {
	requireShell(t)
	s := startTTY(t, NewManager(), "stty size")
	if !s.Wait(context.Background(), 5*time.Second, WaitExit) {
		t.Fatal("session did not exit")
	}
	out := s.ReadNew()
	if !strings.Contains(out, "24 80") {
		t.Fatalf("stty size output = %q, want 24 80", out)
	}
}

// Arbitrary input is writable on a tty session, and the terminal echoes it.
func TestTTYInteractiveInput(t *testing.T) {
	requireShell(t)
	s := startTTY(t, NewManager(), `read -r x; echo "got:$x"`)
	time.Sleep(150 * time.Millisecond)
	if err := s.SendInput("hello\n"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if !s.Wait(context.Background(), 5*time.Second, WaitExit) {
		t.Fatal("session did not exit after input")
	}
	out := s.ReadNew()
	if !strings.Contains(out, "got:hello") {
		t.Fatalf("output = %q, want got:hello", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("output = %q, want the terminal echo of the input", out)
	}
}

// Pipe sessions reject SendInput; the model must run bash with tty=true.
func TestPipeSessionRejectsInput(t *testing.T) {
	requireShell(t)
	m := NewManager()
	s, err := m.Start(StartRequest{Command: "sleep 30", Argv: bashArgv("sleep 30")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Kill()
	if err := s.SendInput("x\n"); err == nil {
		t.Fatal("SendInput on a pipe session = nil error, want failure")
	}
}

// Ctrl-C delivery is identical for tty sessions: SIGINT first, kill on the
// second request for a process that ignores it.
func TestTTYInterruptEscalates(t *testing.T) {
	requireShell(t)
	s := startTTY(t, NewManager(), "trap '' INT TERM; while :; do sleep 1; done")
	time.Sleep(150 * time.Millisecond)
	s.Interrupt()
	time.Sleep(300 * time.Millisecond)
	if s.Exited() {
		t.Fatal("tty session exited on the first interrupt despite trapping SIGINT")
	}
	s.Interrupt()
	if !s.Wait(context.Background(), 3*time.Second, WaitExit) {
		t.Fatal("tty session survived the second interrupt")
	}
}

// A plain tty command exits normally and its post-exit output is not lost.
func TestTTYRunToExit(t *testing.T) {
	requireShell(t)
	s := startTTY(t, NewManager(), "echo done")
	if !s.Wait(context.Background(), 5*time.Second, WaitExit) {
		t.Fatal("session did not exit")
	}
	out := s.ReadNew()
	if !strings.Contains(out, "done") {
		t.Fatalf("output = %q, want done", out)
	}
	if snap := s.Snapshot(); snap.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", snap.ExitCode)
	}
}

// SendInput after the session exits fails instead of writing to a recycled fd.
func TestTTYInputAfterExit(t *testing.T) {
	requireShell(t)
	s := startTTY(t, NewManager(), "true")
	if !s.Wait(context.Background(), 5*time.Second, WaitExit) {
		t.Fatal("session did not exit")
	}
	if err := s.SendInput("x\n"); err == nil {
		t.Fatal("SendInput after exit = nil error, want failure")
	}
}
