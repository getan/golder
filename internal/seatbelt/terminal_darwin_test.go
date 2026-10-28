//go:build darwin

package seatbelt

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestOwnTerminalProfileHasExactDeviceAndNumericRequestWhitelist(t *testing.T) {
	rule := ownTerminalProfile()
	if !strings.Contains(rule, `(require-all (require-any (literal (param "GOLDER_SELF_PTY")) (literal (param "GOLDER_SELF_CTTY_ALIAS")))`) {
		t.Fatalf("terminal ioctl allowance is not bound to a single child device: %s", rule)
	}
	for _, request := range []uint{unix.TIOCGETA, unix.TIOCSETA, unix.TIOCSETAW, unix.TIOCSETAF, unix.TIOCGWINSZ} {
		if !strings.Contains(rule, fmt.Sprintf("(ioctl-command %d)", request)) {
			t.Errorf("missing necessary terminal request %d", request)
		}
	}
	for _, request := range []uint{unix.TIOCSTI, unix.TIOCSETD, unix.TIOCCONS} {
		if strings.Contains(rule, fmt.Sprintf("(ioctl-command %d)", request)) {
			t.Errorf("unsafe terminal request %d was allowed", request)
		}
	}
	if strings.Contains(rule, "(regex") || strings.Contains(rule, "(subpath") || strings.Contains(rule, "TIOC") {
		t.Fatalf("terminal policy widened device scope or relied on OS symbol definitions: %s", rule)
	}
	runner := &SeatbeltRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	profile, err := runner.writeCallProfile(CallPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(profile)
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "file-ioctl") || strings.Contains(string(data), selfPTYParameter) {
		t.Fatal("non-terminal tools received terminal control")
	}
}

func TestTerminalBindingRejectsClosedStdinAndMalformedBootstrap(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if path, err := ownPTYPath(f); err != nil || path != "" {
		t.Fatalf("non-TTY received a terminal binding: %q, %v", path, err)
	}
	f.Close()
	if _, err := ownPTYPath(f); err == nil {
		t.Fatal("invalid stdin was silently treated as a non-TTY")
	}
	for _, args := range [][]string{nil, {"relative.sb", "--", "true"}, {"/tmp/profile.sb", "--"}, {"/tmp/profile.sb", "bad", "true"}} {
		if err := ExecTerminalSandbox(args); err == nil {
			t.Fatalf("malformed bootstrap executed: %v", args)
		}
	}
}

func TestControllingTTYAliasRejectsBorrowedUnassignedSlave(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if path, err := ownPTYPath(slave); err != nil || path != slave.Name() {
		t.Fatalf("PTY identity baseline failed: %q, %v", path, err)
	}
	if alias, err := ownControllingTTYAlias(slave); err == nil || alias != "" {
		t.Fatalf("an unassigned/borrowed slave authorized a controlling terminal: %q, %v", alias, err)
	}
}

func TestTerminalLaunchWithoutRegisteredBootstrapFailsClosed(t *testing.T) {
	terminalBootstrap.RLock()
	previous := terminalBootstrap.executable
	terminalBootstrap.RUnlock()
	t.Cleanup(func() { _ = ConfigureTerminalLauncher(previous) })
	if err := ConfigureTerminalLauncher(""); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	r := &SeatbeltRunner{ProjectDir: t.TempDir(), TmpDir: tmp}
	argv, cleanup, err := r.SandboxCall([]string{"/usr/bin/true"}, "", CallPolicy{OwnTerminal: true})
	if err == nil || len(argv) != 0 || cleanup != nil {
		t.Fatal("unconfigured SDK host silently ran without a terminal binding")
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatal("failed terminal launch leaked its temporary policy")
	}
}
