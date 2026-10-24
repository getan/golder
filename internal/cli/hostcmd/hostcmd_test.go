package hostcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run drives Run with a captured stdout/stderr and a temporary config home, so
// nothing in these tests touches the person's real ~/.config.
func run(t *testing.T, env map[string]string, cmd string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	home := t.TempDir()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "XDG_CONFIG_HOME" || k == "TUIOS_HARNESS_DIR" || strings.HasPrefix(k, "TUIOS_") ||
			strings.HasPrefix(k, "HERDR_") || k == "GOLDER_HOST_REPORT" || k == "GOLDER_WORKSPACE" ||
			k == "TMUX" || k == "ZELLIJ" || k == "ZELLIJ_SESSION_NAME" || k == "STY" {
			t.Setenv(k, "")
		}
	}
	t.Setenv("XDG_CONFIG_HOME", home)
	for k, v := range env {
		t.Setenv(k, v)
	}

	var out, errOut bytes.Buffer
	code = Run(cmd, args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// TestStatusOutsideAHost says plainly that nothing is reported, which is the
// common case for a person running golder in a plain terminal.
func TestStatusOutsideAHost(t *testing.T) {
	out, _, code := run(t, nil, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("status outside a host pane = %q, want it to say none", out)
	}
	if !strings.Contains(out, "manifest") {
		t.Errorf("status = %q, want the manifest half reported", out)
	}
}

// TestStatusInsideTuiosnamesTheVariables: a person diagnosing "why is golder
// not on my rail" needs to see which variable is missing.
func TestStatusInsideTuios(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"TUIOS_ENV":     "1",
		"TUIOS_PANE_ID": "pane-1",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"tuios", "TUIOS_PANE_ID=pane-1", "not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
}

// TestStatusInsideHerdrSaysNoManifestIsNeeded: herdr is identified by the
// report, so a manifest there is a non-sequitur rather than a missing step.
func TestStatusInsideHerdr(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"HERDR_ENV":      "1",
		"HERDR_PANE_ID":  "w1:p2",
		"HERDR_BIN_PATH": "/usr/local/bin/herdr",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"herdr", "not needed"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
	if strings.Contains(out, "run 'golder host-compat install'") {
		t.Errorf("status inside herdr should not tell the person to install a manifest:\n%s", out)
	}
}

// TestStatusSaysWhenReportingIsOff: the escape hatch is silent by design, so
// the status command has to surface it or a person would chase a phantom.
//
// The name printed here is the name hostcompat.Resolve hands Open, so a person
// reading it is reading what golder actually does.
func TestStatusSaysWhenReportingIsOff(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"TUIOS_ENV":          "1",
		"TUIOS_PANE_ID":      "pane-1",
		"GOLDER_HOST_REPORT": "off",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "OFF") {
		t.Errorf("status = %q, want reporting reported as off", out)
	}
}

// TestStatusInsideGolderWorkspace: a pane of golder's own workspace needs no
// tuios binary and no manifest — the daemon is golder, and it reads the report
// itself. The variables it reports through are still named, because a missing
// one is the usual reason nothing shows up.
func TestStatusInsideGolderWorkspace(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"GOLDER_WORKSPACE": "1",
		"TUIOS_ENV":        "1",
		"TUIOS_SOCKET":     "/run/user/1000/golder/workspace.sock",
		"TUIOS_SESSION":    "work",
		"TUIOS_PANE_ID":    "pane-1",
		"TUIOS_PANE_TOKEN": "proof",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{
		"host:        golder-workspace",
		"TUIOS_SOCKET=/run/user/1000/golder/workspace.sock",
		"TUIOS_PANE_ID=pane-1",
		"TUIOS_PANE_TOKEN=proof",
		"manifest:    not needed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
	if strings.Contains(out, "golder host-compat install") {
		t.Errorf("a workspace pane needs no manifest installed:\n%s", out)
	}
}

// TestStatusInAWorkspaceMissingItsSocket: the workspace host reports over its
// daemon's socket, so a pane without one says exactly that instead of naming a
// host it cannot reach.
func TestStatusInAWorkspaceMissingItsSocket(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"GOLDER_WORKSPACE": "1",
		"TUIOS_PANE_ID":    "pane-1",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "this is a golder-workspace pane") || !strings.Contains(out, "TUIOS_SOCKET is not set") {
		t.Errorf("status = %q, want it to name the missing socket", out)
	}
	if strings.Contains(out, "host:        golder-workspace") {
		t.Errorf("status claims a host it cannot reach:\n%s", out)
	}
}

// TestStatusInTmuxNamesTheTerminal: golder recognises tmux, zellij and screen
// and has no driver for any of them. Saying which one it found is more useful
// than a bare "none", and it must not pretend to report.
func TestStatusInTmuxNamesTheTerminal(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"tmux":   {"TMUX": "/tmp/tmux-1000/default,42,0"},
		"zellij": {"ZELLIJ": "0"},
		"screen": {"STY": "1234.pts-0.laptop"},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, code := run(t, kv, "status")
			if code != 0 {
				t.Fatalf("exit = %d, want 0", code)
			}
			if !strings.Contains(out, "no driver for "+name) {
				t.Errorf("status = %q, want it to say there is no %s driver", out, name)
			}
			if strings.Contains(out, "host:        "+name) {
				t.Errorf("status claims to report to %s:\n%s", name, out)
			}
		})
	}
}

// TestStatusForcedHostInAPlainTerminal: the escape hatch names a host the pane
// cannot carry, so status says that and what is missing rather than claiming
// the host is in use.
func TestStatusForcedHostInAPlainTerminal(t *testing.T) {
	out, _, code := run(t, map[string]string{
		"GOLDER_HOST_REPORT": "tuios",
	}, "status")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"GOLDER_HOST_REPORT=tuios", "cannot reach", "TUIOS_PANE_ID is not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to mention %q", out, want)
		}
	}
}

// TestInstallThenStatusThenUninstall drives the documented cycle end to end
// through the CLI surface a person actually types.
func TestInstallThenStatusThenUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("TUIOS_HARNESS_DIR", "")
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_PANE_ID", "pane-1")

	var out, errOut bytes.Buffer
	if code := Run("install", nil, &out, &errOut); code != 0 {
		t.Fatalf("install exit = %d, stderr = %q", code, errOut.String())
	}
	path := filepath.Join(home, "tuios", "harnesses", "golder.toml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install did not write %s: %v", path, err)
	}
	if !strings.Contains(out.String(), "kill-server") {
		t.Errorf("install = %q, want it to say the daemon must be restarted", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := Run("status", nil, &out, &errOut); code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(out.String(), "installed") {
		t.Errorf("status after install = %q, want installed", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := Run("uninstall", nil, &out, &errOut); code != 0 {
		t.Fatalf("uninstall exit = %d, stderr = %q", code, errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uninstall left %s behind: %v", path, err)
	}
}

// TestInstallPrintWritesTheManifestToStdout: the escape hatch for a person who
// would rather place the file themselves.
func TestInstallPrintWritesTheManifestToStdout(t *testing.T) {
	out, _, code := run(t, nil, "install", "--print")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, `id             = "golder"`) {
		t.Errorf("install --print did not write the manifest:\n%s", out)
	}
	// --print must not install anything.
	if _, err := os.Stat(filepath.Join(t.TempDir(), "tuios")); err == nil {
		t.Error("install --print should not touch the filesystem")
	}
}

// TestUnknownCommandIsAUsageError, so a typo says what to type instead.
func TestUnknownCommandIsAUsageError(t *testing.T) {
	_, errOut, code := run(t, nil, "stauts")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q, want an unknown-command message", errOut)
	}

	_, errOut, code = run(t, nil, "")
	if code != 2 {
		t.Errorf("no subcommand: exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "usage:") {
		t.Errorf("stderr = %q, want usage", errOut)
	}
}

// TestInstallRefusesAnUnknownFlag keeps a typo from being taken as a flag.
func TestInstallRefusesAnUnknownFlag(t *testing.T) {
	_, errOut, code := run(t, nil, "install", "--dryrun")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Errorf("stderr = %q, want an unknown-flag message", errOut)
	}
}
