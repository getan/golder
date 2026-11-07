//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/getan/golder/internal/seatbelt"
)

func TestTerminalLauncherRegistration(t *testing.T) {
	if err := configureTerminalLauncher(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seatbelt.ConfigureTerminalLauncher("") })
	runner := &seatbelt.SeatbeltRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	argv, cleanup, err := runner.SandboxCall([]string{"/usr/bin/true"}, "", seatbelt.CallPolicy{OwnTerminal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 5 || argv[0] != executable || argv[1] != seatbelt.TerminalBootstrapFlag || argv[3] != "--" || argv[4] != "/usr/bin/true" {
		t.Fatalf("unexpected terminal bootstrap: %q", argv)
	}
}

func TestTerminalBootstrapDispatchedBeforeGUIStartup(t *testing.T) {
	if os.Getenv("GOLDER_TEST_GUI_BOOTSTRAP") == "1" {
		os.Args = []string{os.Args[0], seatbelt.TerminalBootstrapFlag, "invalid"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalBootstrapDispatchedBeforeGUIStartup$")
	cmd.Env = append(os.Environ(), "GOLDER_TEST_GUI_BOOTSTRAP=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "terminal sandbox: invalid terminal sandbox bootstrap arguments") {
		t.Fatalf("bootstrap was not dispatched before flag parsing: err=%v output=%s", err, out)
	}
}
