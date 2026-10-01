package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestFormatElapsed checks the compact duration formatting across the second,
// minute, and hour ranges.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{114 * time.Second, "1m 54s"},
		{60 * time.Minute, "1h 0m"},
		{62 * time.Minute, "1h 2m"},
		{-3 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestSpinnerViewStats verifies a running spinner renders its verb with an
// ellipsis and the elapsed/token/effort stats, and that a stopped spinner
// renders nothing.
func TestSpinnerViewStats(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now().Add(-114*time.Second), "medium")
	s.chars = 968 // 968/4 = 242 estimated tokens

	view := stripANSI(s.view(120))
	if !strings.Contains(view, s.verb+"…") {
		t.Errorf("view %q should contain the verb with an ellipsis", view)
	}
	for _, want := range []string{"1m 54s", "↓ 242 tokens", "medium effort"} {
		if !strings.Contains(view, want) {
			t.Errorf("view %q missing stat %q", view, want)
		}
	}

	s.stop()
	if got := s.view(120); got != "" {
		t.Errorf("stopped spinner should render nothing, got %q", got)
	}
}

// TestSpinnerPinOverridesVerb verifies a pinned label replaces the random verb
// and survives verb re-rolls, and that unpin restores the cycling verb.
func TestSpinnerPinOverridesVerb(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now(), "")
	s.pin("Compacting conversation")

	// Advance well past the re-roll interval: a pinned label must not change.
	for i := 0; i < verbRerollFrames*2; i++ {
		s.advance()
	}
	view := stripANSI(s.view(120))
	if !strings.Contains(view, "Compacting conversation…") {
		t.Errorf("pinned spinner view %q should show the pinned label", view)
	}

	s.unpin()
	if got := stripANSI(s.view(120)); strings.Contains(got, "Compacting conversation") {
		t.Errorf("after unpin, view %q should not show the pinned label", got)
	}
}

// tokens stream and the effort stat is hidden with no thinking level.
func TestSpinnerViewOmitsEmptyStats(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now(), "")

	view := stripANSI(s.view(120))
	if strings.Contains(view, "tokens") {
		t.Errorf("view %q should not show a token stat before any deltas", view)
	}
	if strings.Contains(view, "effort") {
		t.Errorf("view %q should not show an effort stat with no thinking level", view)
	}
}

// TestSpinnerAdvanceRerollsVerb verifies the animation frame advances and the
// verb is re-picked on the reroll cadence.
func TestSpinnerAdvanceRerollsVerb(t *testing.T) {
	s := newSpinner(DefaultTheme())
	s.begin(time.Now(), "")
	if s.frame != 0 {
		t.Fatalf("fresh spinner frame = %d, want 0", s.frame)
	}
	for i := 0; i < verbRerollFrames; i++ {
		s.advance()
	}
	if s.frame != verbRerollFrames {
		t.Errorf("frame after %d advances = %d", verbRerollFrames, s.frame)
	}
}

// TestModelRunningShowsSpinnerRow verifies that while a run is in flight the
// spinner occupies its own row above the input and the shell still fills exactly
// the terminal height (relayout shrinks the transcript by the spinner row).
func TestModelRunningShowsSpinnerRow(t *testing.T) {
	m := apply(t, NewModel(Options{Model: "test-model"}), tea.WindowSizeMsg{Width: 60, Height: 10})
	m.running = true
	m.spinner.begin(time.Now(), "medium")
	m.relayout()

	view, _ := m.renderContent()
	if got := strings.Count(view, "\n"); got != 9 {
		t.Errorf("running newline count = %d, want 9 (10 rows)", got)
	}
	if !strings.Contains(stripANSI(view), m.spinner.verb+"…") {
		t.Errorf("running view should contain the spinner verb, got:\n%s", stripANSI(view))
	}
}
