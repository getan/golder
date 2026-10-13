package hostcompat

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

// seqOf pulls the value after --seq out of an argv.
func seqOf(t *testing.T, argv []string) uint64 {
	t.Helper()
	for i, a := range argv {
		if a != "--seq" || i+1 >= len(argv) {
			continue
		}
		n, err := strconv.ParseUint(argv[i+1], 10, 64)
		if err != nil {
			t.Fatalf("--seq %q is not a number: %v", argv[i+1], err)
		}
		return n
	}
	t.Fatalf("no --seq in %q", argv)
	return 0
}

// TestHerdrStateArgv pins the exact command line a herdr report becomes.
func TestHerdrStateArgv(t *testing.T) {
	cases := []struct {
		name  string
		state State
		msg   string
		sess  SessionRef
		want  func(seq uint64) []string
	}{
		{
			name:  "working",
			state: StateWorking,
			want: func(seq uint64) []string {
				return []string{
					"/usr/local/bin/herdr", "pane", "report-agent", "w1:p2",
					"--source", "golder", "--agent", "golder",
					"--state", "working", "--seq", strconv.FormatUint(seq, 10),
				}
			},
		},
		{
			name:  "blocked carries the question",
			state: StateBlocked,
			msg:   "approve Bash: git push --force",
			want: func(seq uint64) []string {
				return []string{
					"/usr/local/bin/herdr", "pane", "report-agent", "w1:p2",
					"--source", "golder", "--agent", "golder",
					"--state", "blocked", "--seq", strconv.FormatUint(seq, 10),
					"--message", "approve Bash: git push --force",
				}
			},
		},
		{
			name:  "done folds to idle and says so",
			state: StateDone,
			want: func(seq uint64) []string {
				return []string{
					"/usr/local/bin/herdr", "pane", "report-agent", "w1:p2",
					"--source", "golder", "--agent", "golder",
					"--state", "idle", "--seq", strconv.FormatUint(seq, 10),
					"--message", "turn finished",
				}
			},
		},
		{
			name:  "the conversation and its resume command ride the report",
			state: StateIdle,
			sess:  SessionRef{ID: "20261011-231500-000123", ResumeArgv: []string{"golder", "--resume", "20261011-231500-000123"}},
			want: func(seq uint64) []string {
				return []string{
					"/usr/local/bin/herdr", "pane", "report-agent", "w1:p2",
					"--source", "golder", "--agent", "golder",
					"--state", "idle", "--seq", strconv.FormatUint(seq, 10),
					"--agent-session-id", "20261011-231500-000123",
					"--", "golder", "--resume", "20261011-231500-000123",
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder()
			rep := openFor(t, herdrPane(), rec)
			// A fixed clock, so the sequence number in the expectation is the
			// one the test itself computed.
			rep.now = func() time.Time { return time.Unix(0, 1750000000000000000) }
			if err := rep.host.Apply(t.Context(), Report{State: tc.state, Message: tc.msg}, tc.sess); err != nil {
				t.Fatal(err)
			}
			got := rec.next(t)
			want := tc.want(1750000000000000000)
			if !slices.Equal(got, want) {
				t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestHerdrSeqAlwaysAdvances is the host's own rule: herdr drops a report whose
// seq is not above the last one it accepted, so two reports in the same
// nanosecond (or behind a clock that stepped backwards) must still be ordered.
// A reporter that reused a seq would go silently stale on the rail.
func TestHerdrSeqAlwaysAdvances(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, herdrPane(), rec)
	frozen := time.Unix(0, 1750000000000000000)
	rep.now = func() time.Time { return frozen }

	for i := range 5 {
		if err := rep.host.Apply(t.Context(), Report{State: StateWorking}, SessionRef{}); err != nil {
			t.Fatal(err)
		}
		got := rec.next(t)
		seq := seqOf(t, got)
		want := uint64(1750000000000000000 + i)
		if seq != want {
			t.Fatalf("report %d has seq %d, want %d (a frozen clock must still advance)", i, seq, want)
		}
	}

	// And a clock that steps backwards cannot move the sequence back either.
	rep.now = func() time.Time { return frozen.Add(-time.Hour) }
	if err := rep.host.Apply(t.Context(), Report{State: StateIdle}, SessionRef{}); err != nil {
		t.Fatal(err)
	}
	seq := seqOf(t, rec.next(t))
	if seq <= 1750000000000000004 {
		t.Errorf("seq went backwards to %d after the clock jumped back", seq)
	}
}

// TestHerdrSessionOnlyArgv: report-agent-session names the conversation without
// moving the pane's state.
func TestHerdrSessionOnlyArgv(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, herdrPane(), rec)
	rep.now = func() time.Time { return time.Unix(0, 1750000000000000000) }
	sess := SessionRef{ID: "20261011-231500-000123", ResumeArgv: []string{"golder", "--resume", "20261011-231500-000123"}}
	if err := rep.host.Apply(t.Context(), Report{}, sess); err != nil {
		t.Fatal(err)
	}
	got := rec.next(t)
	want := []string{
		"/usr/local/bin/herdr", "pane", "report-agent-session", "w1:p2",
		"--source", "golder", "--agent", "golder",
		"--seq", "1750000000000000000",
		"--agent-session-id", "20261011-231500-000123",
		"--", "golder", "--resume", "20261011-231500-000123",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestHerdrReleaseUsesTheReleaseVerb: herdr has one, so the pane's name, state
// and resume command are cleared at once instead of waiting for herdr to notice
// the shell prompt.
func TestHerdrReleaseUsesTheReleaseVerb(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, herdrPane(), rec)
	rep.now = func() time.Time { return time.Unix(0, 1750000000000000000) }
	if err := rep.host.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := rec.next(t)
	want := []string{
		"/usr/local/bin/herdr", "pane", "release-agent", "w1:p2",
		"--source", "golder", "--agent", "golder",
		"--seq", "1750000000000000000",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestHerdrMetaArgv: metadata goes through report-metadata as tokens with a
// lifetime, and unknown keys are dropped.
func TestHerdrMetaArgv(t *testing.T) {
	rec := newRecorder()
	rep := openFor(t, herdrPane(), rec)
	rep.now = func() time.Time { return time.Unix(0, 1750000000000000000) }
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
		"/usr/local/bin/herdr", "pane", "report-metadata", "w1:p2",
		"--source", "golder", "--agent", "golder",
		"--seq", "1750000000000000000",
		"--ttl-ms", "90000",
		"--token", "model=deepseek-v4.1-flash",
		"--token", "context=42%",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestHerdrUsesItsOwnBinary: herdr names the binary that owns the pane, so
// there is no PATH lookup to get wrong — and a pane without it reports nothing.
func TestHerdrUsesItsOwnBinary(t *testing.T) {
	rep := Open(Options{
		Getenv: env(map[string]string{
			"HERDR_ENV":     "1",
			"HERDR_PANE_ID": "w1:p2",
			// HERDR_BIN_PATH missing.
		}),
		Run: newRecorder().run,
	})
	if rep != nil {
		t.Error("a herdr pane with no HERDR_BIN_PATH should not report")
	}
}

// TestHerdrSourceIsNotHerdrsNamespace: herdr keeps its own integrations under
// "herdr:", and its docs ask a vendor's integration to use something else — a
// report under that prefix would be read as one of herdr's own.
func TestHerdrSourceIsNotHerdrsNamespace(t *testing.T) {
	if got := herdrSource; got != "golder" {
		t.Errorf("herdr source = %q, want %q", got, "golder")
	}
}
