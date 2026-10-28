//go:build darwin

package agenttool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/seatbelt"
	"golang.org/x/sys/unix"
)

const syntheticTerminalInput = "fixture-only-not-a-credential"
const terminalChildMarker = "--controlled-pane-terminal-fixture"

// Enter only in a test subprocess, after the real pane sandbox was applied.
func TestControlledPaneTerminalChild(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == terminalChildMarker {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	fd := int(os.Stdin.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.IoctlSetTermios(fd, unix.TIOCSETA, original)
	if len(os.Args) > index+1 {
		// A second disposable PTY is readable under the runtime whitelist, but
		// even an already-open descriptor must not authorize its ioctls.
		other, err := os.Open(os.Args[index+1])
		if err != nil {
			t.Fatal("foreign-PTY read baseline failed:", err)
		}
		defer other.Close()
		changed := *original
		changed.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(int(other.Fd()), unix.TIOCSETA, &changed); err != unix.EPERM {
			t.Fatalf("foreign terminal ioctl was not denied by Seatbelt: %v", err)
		}
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSETD, 0); err != unix.EPERM {
			t.Fatalf("line-discipline mutation was not denied: %v", err)
		}
		var dummy byte = 'x'
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCSTI, uintptr(unsafe.Pointer(&dummy)))
		if errno != unix.EPERM {
			t.Fatalf("input injection was not denied: %v", errno)
		}
		fmt.Println("FOREIGN_AND_UNSAFE_DENIED")
		return
	}
	noEcho := *original
	noEcho.Lflag &^= unix.ECHO | unix.ECHONL
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &noEcho); err != nil {
		t.Fatal("cannot disable echo; no input requested:", err)
	}
	verified, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil || verified.Lflag&(unix.ECHO|unix.ECHONL) != 0 {
		t.Fatal("echo disable could not be verified; no input requested")
	}
	// Also exercise canonical/raw switching without leaving the terminal raw
	// when the synthetic line is read below.
	raw := noEcho
	raw.Lflag &^= unix.ICANON
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw); err != nil {
		t.Fatal(err)
	}
	state, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil || state.Lflag&unix.ICANON != 0 {
		t.Fatal("raw mode was not applied")
	}
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &noEcho); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ECHO_DISABLED_VERIFIED")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || strings.TrimSuffix(line, "\n") != syntheticTerminalInput {
		t.Fatal("synthetic terminal read failed (contents withheld)")
	}
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, original); err != nil {
		t.Fatal(err)
	}
	state, err = unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil || state.Lflag != original.Lflag {
		t.Fatal("terminal echo was not restored")
	}
	fmt.Println("ECHO_RESTORED")
}

func terminalFixture(t *testing.T, other ...string) *execsess.Session {
	t.Helper()
	if !seatbelt.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	g := NewControlGuard(t.TempDir())
	child := []string{os.Args[0], "-test.run=^TestControlledPaneTerminalChild$", "--", terminalChildMarker}
	child = append(child, other...)
	argv, cleanup, isolated, err := g.launchArgv(context.Background(), child, g.Root, false, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if !isolated {
		t.Fatal("a terminal test escaped its real sandbox")
	}
	s, err := execsess.NewManager().Start(execsess.StartRequest{Argv: argv, Dir: g.Root, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Kill)
	return s
}

func TestControlledPaneVerifiesNoEchoBeforeInputAndRestores(t *testing.T) {
	s := terminalFixture(t)
	var out strings.Builder
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "ECHO_DISABLED_VERIFIED") {
		out.WriteString(s.ReadNew())
		if s.Exited() || time.Now().After(deadline) {
			t.Fatalf("echo was not verified; no input sent: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Only a fixed synthetic fixture is sent, and only after the child has
	// independently checked the actual termios ECHO bits. Never a real secret.
	if err := s.SendInput(syntheticTerminalInput + "\n"); err != nil {
		t.Fatal(err)
	}
	if !s.Wait(context.Background(), 3*time.Second, execsess.WaitExit) {
		t.Fatal("fixture did not finish")
	}
	out.WriteString(s.ReadNew())
	if s.Snapshot().ExitCode != 0 || !strings.Contains(out.String(), "ECHO_RESTORED") || strings.Contains(out.String(), syntheticTerminalInput) {
		t.Fatalf("input was echoed, failed, or terminal was not restored: %q", out.String())
	}
}

func TestControlledPaneCannotControlAnotherPTYOrInjectInput(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.IoctlSetTermios(int(slave.Fd()), unix.TIOCSETA, before)
	s := terminalFixture(t, slave.Name())
	if !s.Wait(context.Background(), 3*time.Second, execsess.WaitExit) {
		t.Fatal("boundary fixture did not finish")
	}
	out := s.ReadNew()
	if s.Snapshot().ExitCode != 0 || !strings.Contains(out, "FOREIGN_AND_UNSAFE_DENIED") {
		t.Fatalf("terminal boundary failed: %q", out)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TIOCGETA)
	if err != nil || *before != *after {
		t.Fatal("another pane's terminal state was modified")
	}
}
