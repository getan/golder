package hostcompat

import (
	"slices"
	"testing"
)

// TestTuiosStateArgv pins the exact command line a tuios report becomes. The
// verbs are tuios's published contract, so a change here is a change to what
// that host is told — which is why it is asserted literally rather than by
// shape.
func TestTuiosStateArgv(t *testing.T) {
	cases := []struct {
		name  string
		state State
		kind  Kind
		msg   string
		sess  SessionRef
		want  []string
	}{
		{
			name:  "working, no extras",
			state: StateWorking,
			want:  []string{"/usr/local/bin/tuios", "set-agent-state", "working", "-w", "98db8226-1829-468e-89a8-41a2baa0ddab", "--harness", "golder"},
		},
		{
			name:  "blocked becomes needs_input, with its kind",
			state: StateBlocked,
			kind:  KindApproval,
			msg:   "approve Bash: rm -rf build",
			want: []string{
				"/usr/local/bin/tuios", "set-agent-state", "needs_input",
				"-w", "98db8226-1829-468e-89a8-41a2baa0ddab",
				"--harness", "golder",
				"--kind", "approval",
				"-m", "approve Bash: rm -rf build",
			},
		},
		{
			name:  "done carries the conversation id",
			state: StateDone,
			sess:  SessionRef{ID: "20261011-231500-000123"},
			want: []string{
				"/usr/local/bin/tuios", "set-agent-state", "done",
				"-w", "98db8226-1829-468e-89a8-41a2baa0ddab",
				"--harness", "golder",
				"--agent-session-id", "20261011-231500-000123",
			},
		},
		{
			name:  "errored is tuios's own name for a failed stop",
			state: StateErrored,
			want:  []string{"/usr/local/bin/tuios", "set-agent-state", "errored", "-w", "98db8226-1829-468e-89a8-41a2baa0ddab", "--harness", "golder"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder()
			rep := openFor(t, tuiosPane(), rec)
			rep.host.Apply(t.Context(), Report{State: tc.state, Kind: tc.kind, Message: tc.msg}, tc.sess)
			got := rec.next(t)
			if !slices.Equal(got, tc.want) {
				t.Errorf("argv mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestTuiosSessionOnlyArgv: a report with no state names the conversation
// through the verb tuios keeps for exactly that, because a state report would
// move the pane.
func TestTuiosSessionOnlyArgv(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, tuiosPane(), rec)
	rep.host.Apply(t.Context(), Report{}, SessionRef{ID: "20261011-231500-000123"})
	got := rec.next(t)
	want := []string{
		"/usr/local/bin/tuios", "set-agent-session",
		"-w", "98db8226-1829-468e-89a8-41a2baa0ddab",
		"--harness", "golder",
		"20261011-231500-000123",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestTuiosReleaseIsNone: tuios has no release verb, so clearing the state is
// the release. The pane goes back to being a shell.
func TestTuiosReleaseIsNone(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, tuiosPane(), rec)
	if err := rep.host.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := rec.next(t)
	want := []string{"/usr/local/bin/tuios", "set-agent-state", "none", "-w", "98db8226-1829-468e-89a8-41a2baa0ddab", "--harness", "golder"}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestTuiosMetaArgv: metadata goes through set-agent-meta with a lifetime, so
// a feed that stops writing leaves nothing stale on the rail, and unknown keys
// are dropped rather than sent.
func TestTuiosMetaArgv(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, tuiosPane(), rec)
	err := rep.host.ApplyMeta(t.Context(), map[string]string{
		"model":   "deepseek-v4.1-flash",
		"context": "42%",
		"bogus":   "dropped",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rec.next(t)
	want := []string{
		"/usr/local/bin/tuios", "set-agent-meta",
		"-w", "98db8226-1829-468e-89a8-41a2baa0ddab",
		"--source", "golder",
		"--ttl", "90s",
		"model=deepseek-v4.1-flash",
		"context=42%",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestTuiosMetaCutsLongValues: tuios cuts a value at 80 characters, and a
// value cut mid-rune would be mojibake on the rail.
func TestTuiosMetaCutsLongValues(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, tuiosPane(), rec)
	long := ""
	for range 200 {
		long += "中" // 3 bytes each: a byte cut would split it
	}
	if err := rep.host.ApplyMeta(t.Context(), map[string]string{"summary": long}); err != nil {
		t.Fatal(err)
	}
	got := rec.next(t)
	last := got[len(got)-1]
	value, _ := cutAfter(last)
	if n := len([]rune(value)); n != tuiosMetaMaxValue {
		t.Errorf("value has %d runes, want %d", n, tuiosMetaMaxValue)
	}
	if !isValidUTF8(value) {
		t.Errorf("value is not valid UTF-8: %q", value)
	}
}

// cutAfter splits "key=value" at its first "=".
func cutAfter(s string) (value string, ok bool) {
	for i := range len(s) {
		if s[i] == '=' {
			return s[i+1:], true
		}
	}
	return "", false
}

// isValidUTF8 guards the rune-cut test without importing unicode/utf8 twice.
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// TestTuiosNeedsThePaneId: a process inside tuios but not in a pane (a client,
// say) has no pane to report about, so it reports nothing rather than guessing
// at the focused window.
func TestTuiosNeedsThePaneId(t *testing.T) {
	rep := Open(Options{
		Getenv: env(map[string]string{
			"TUIOS_ENV":   "1",
			EnvHostReport: "tuios",
		}),
		LookPath: fakeLookPath,
		Run:      newRecorder().run,
	})
	if rep != nil {
		t.Error("a tuios process with no TUIOS_PANE_ID should not report")
	}
}
