package workspacecmd

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/hostcompat"
)

// nestsIn lists the environment that puts this process inside each multiplexer
// golder can recognise, and the name it is printed under. The markers are
// hostcompat's own: the same variables, read the same way, so detection here
// cannot disagree with detection there.
func nestsIn() map[string]map[string]string {
	return map[string]map[string]string{
		hostcompat.MuxGolder: {
			"GOLDER_WORKSPACE": "1",
			"TUIOS_ENV":        "1",
			"TUIOS_SOCKET":     "/run/user/1000/golder/ws.sock",
			"TUIOS_PANE_ID":    "pane-1",
		},
		hostcompat.MuxTuios: {
			"TUIOS_ENV":     "1",
			"TUIOS_PANE_ID": "pane-1",
		},
		hostcompat.MuxHerdr: {
			"HERDR_ENV":      "1",
			"HERDR_PANE_ID":  "w1:p2",
			"HERDR_BIN_PATH": "/usr/local/bin/herdr",
		},
		hostcompat.MuxTmux:   {"TMUX": "/tmp/tmux-1000/default,42,0"},
		hostcompat.MuxZellij: {"ZELLIJ": "0"},
		hostcompat.MuxScreen: {"STY": "1234.pts-0.laptop"},
	}
}

// TestNestingIsRefusedInsideEveryMultiplexer: starting a workspace inside
// another multiplexer nests one terminal in another, which is almost never what
// someone meant. The command refuses, says which multiplexer it found, and names
// the flag that overrides it — for every host golder can recognise, including
// the ones it has no driver for.
func TestNestingIsRefusedInsideEveryMultiplexer(t *testing.T) {
	for host, kv := range nestsIn() {
		t.Run(host, func(t *testing.T) {
			e := newTestEnv(t)
			for k, v := range kv {
				e.set(k, v)
			}
			rec := &recorder{}
			stdout, stderr, code := e.run(t, rec)
			if code != ExitFailure {
				t.Fatalf("exit = %d, want 1 (stdout: %q)", code, stdout)
			}
			if rec.calls() != 0 {
				t.Error("a refused start reached the backend")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			for _, want := range []string{"--nested", host} {
				if !strings.Contains(stderr, want) && !(host == hostcompat.MuxGolder && want == host) {
					t.Errorf("stderr = %q, want it to mention %q", stderr, want)
				}
			}
		})
	}
}

// TestNestedFlagStartsAnyway: --nested is the person saying they meant it, so
// the workspace starts and the backend learns where it came from.
func TestNestedFlagStartsAnyway(t *testing.T) {
	for host, kv := range nestsIn() {
		t.Run(host, func(t *testing.T) {
			e := newTestEnv(t)
			for k, v := range kv {
				e.set(k, v)
			}
			rec := &recorder{}
			if _, stderr, code := e.run(t, rec, "--nested"); code != ExitOK {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
			}
			got := rec.started[0]
			if !got.Nested {
				t.Error("nested = false, and --nested was given")
			}
			if got.Host != host {
				t.Errorf("host = %q, want %q", got.Host, host)
			}
		})
	}
}

// TestBooleanMarkersAreReadAsWords: a marker is a word, not a number. "0",
// "false", "no", "off" and "" all mean "not here", exactly as hostcompat reads
// them, so a wrapper that exports GOLDER_WORKSPACE=0 does not make golder think
// it is inside its own workspace — and thus does not refuse to start one.
func TestBooleanMarkersAreReadAsWords(t *testing.T) {
	off := []string{"", "0", "false", "no", "off"}
	on := []string{"1", "true", "yes", "on"}

	for _, value := range off {
		t.Run("GOLDER_WORKSPACE="+value, func(t *testing.T) {
			e := newTestEnv(t)
			e.set("GOLDER_WORKSPACE", value)
			rec := &recorder{}
			_, stderr, code := e.run(t, rec)
			if code != ExitOK {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
			}
			if rec.started[0].Host != hostcompat.MuxNone {
				t.Errorf("host = %q, want none", rec.started[0].Host)
			}
		})
	}
	for _, value := range on {
		t.Run("GOLDER_WORKSPACE="+value, func(t *testing.T) {
			e := newTestEnv(t)
			e.set("GOLDER_WORKSPACE", value)
			_, stderr, code := e.run(t, &recorder{})
			if code != ExitFailure {
				t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr)
			}
			if !strings.Contains(stderr, "golder's own workspace") {
				t.Errorf("stderr = %q, want it to name golder's own workspace", stderr)
			}
		})
	}
}

// TestHeadlessDoesNotNestByDefault: --headless is not an escape from the
// nesting rule. A daemon started inside a tmux pane would still draw its panes
// in a terminal it does not own, so it is refused for the same reason, and
// --nested is still the way to say otherwise.
func TestHeadlessDoesNotNestByDefault(t *testing.T) {
	e := newTestEnv(t)
	e.set("TMUX", "/tmp/tmux-1000/default,42,0")
	rec := &recorder{}

	stdout, stderr, code := e.run(t, rec, "--headless")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1 (stdout: %q)", code, stdout)
	}
	if rec.calls() != 0 {
		t.Error("a refused headless start reached the backend")
	}
	if !strings.Contains(stderr, "--nested") {
		t.Errorf("stderr = %q, want it to name --nested", stderr)
	}

	// And with --nested it is a headless start like any other.
	rec = &recorder{}
	stdout, stderr, code = e.run(t, rec, "--headless", "--nested")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !rec.started[0].Headless {
		t.Error("headless = false, and --headless was given")
	}
	if !strings.Contains(stdout, `"headless": true`) {
		t.Errorf("stdout = %q, want a readiness document", stdout)
	}
}

// TestStatusAndStopWorkInsideAMultiplexer: inspecting or stopping a workspace
// from a nested shell is exactly how a person checks on one, so those two are
// never refused.
func TestStatusAndStopWorkInsideAMultiplexer(t *testing.T) {
	kv := nestsIn()[hostcompat.MuxTmux]
	for _, args := range [][]string{{"status"}, {"stop"}} {
		t.Run(args[0], func(t *testing.T) {
			e := newTestEnv(t)
			for k, v := range kv {
				e.set(k, v)
			}
			rec := &recorder{}
			if _, stderr, code := e.run(t, rec, args...); code != ExitOK {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
			}
			if rec.calls() != 1 {
				t.Errorf("backend calls = %d, want one", rec.calls())
			}
		})
	}
}

// TestNestingDoesNotDependOnReporting: GOLDER_HOST_REPORT=off turns reporting
// off, not detection. The multiplexer is still there, so nesting is still
// refused — the two decisions read the same variables and answer different
// questions.
func TestNestingDoesNotDependOnReporting(t *testing.T) {
	e := newTestEnv(t)
	e.set("TMUX", "/tmp/tmux-1000/default,42,0")
	e.set(hostcompat.EnvHostReport, "off")

	_, stderr, code := e.run(t, &recorder{})
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, hostcompat.MuxTmux) {
		t.Errorf("stderr = %q, want it to name tmux", stderr)
	}
}

// TestTheWorkspaceMarkerOutranksTheOthers: golder's own workspace plants the
// TUIOS_* variables too, so a pane of it looks like a tuios pane as well. The
// message names the pane the person is actually in.
func TestTheWorkspaceMarkerOutranksTheOthers(t *testing.T) {
	e := newTestEnv(t)
	e.set("GOLDER_WORKSPACE", "1")
	e.set("TUIOS_ENV", "1")

	_, stderr, code := e.run(t, &recorder{})
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stderr, "golder's own workspace") {
		t.Errorf("stderr = %q, want it to name golder's own workspace rather than tuios", stderr)
	}
}

// TestPlainTerminalIsNotNesting is the other half of the rule: outside every
// multiplexer the command starts a workspace with no flag at all, and reports
// the host as none.
func TestPlainTerminalIsNotNesting(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	if _, stderr, code := e.run(t, rec); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if rec.started[0].Nested {
		t.Error("nested = true in a plain terminal")
	}
	if rec.started[0].Host != hostcompat.MuxNone {
		t.Errorf("host = %q, want none", rec.started[0].Host)
	}
}
