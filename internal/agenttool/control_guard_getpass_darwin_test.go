//go:build darwin

package agenttool

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/seatbelt"
)

// Python getpass reopens /dev/tty instead of necessarily using fd 0. Verify
// that the exact vnode grant also works through that controlling-tty alias.
// Make its plaintext fallback a hard error; this test never accepts it.
func TestControlledPanePythonGetpassWithoutPlaintextFallback(t *testing.T) {
	if !seatbelt.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("optional Python compatibility probe needs python3")
	}
	g := NewControlGuard(t.TempDir())
	script := `import getpass, sys, termios, warnings
warnings.simplefilter("error", getpass.GetPassWarning)
before = termios.tcgetattr(0)
value = getpass.getpass("SYNTHETIC_GETPASS_READY:")
if value != "fixture-only-not-a-credential":
    raise RuntimeError("synthetic input mismatch; contents withheld")
if termios.tcgetattr(0) != before:
    raise RuntimeError("terminal was not restored")
print("GETPASS_RESTORED", flush=True)
`
	argv, cleanup, _, err := g.launchArgv(context.Background(), []string{python, "-I", "-c", script}, g.Root, false, python)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	s, err := execsess.NewManager().Start(execsess.StartRequest{Argv: argv, Dir: g.Root, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill()
	var out strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasPrefix(out.String(), "SYNTHETIC_GETPASS_READY:") {
		out.WriteString(s.ReadNew())
		if s.Exited() || time.Now().After(deadline) {
			t.Fatalf("getpass did not enter no-echo input; nothing sent: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.SendInput(syntheticTerminalInput + "\n"); err != nil {
		t.Fatalf("getpass exited before synthetic input: %v; %q", err, out.String()+s.ReadNew())
	}
	if !s.Wait(context.Background(), 5*time.Second, execsess.WaitExit) {
		t.Fatal("getpass fixture did not exit")
	}
	out.WriteString(s.ReadNew())
	if s.Snapshot().ExitCode != 0 || !strings.Contains(out.String(), "GETPASS_RESTORED") || strings.Contains(out.String(), syntheticTerminalInput) {
		t.Fatalf("getpass used fallback, echoed input or failed restoration: %q", out.String())
	}
}
