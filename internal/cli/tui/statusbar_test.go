package tui

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
)

// newTestStatusBar builds a status bar with a known cwd (already ~-abbreviated
// by the caller's intent) so tests do not depend on the real $HOME.
func newTestStatusBar() statusBar {
	opts := Options{Model: "claude-opus", ThinkingLevel: agentcore.ThinkingHigh}
	s := newStatusBar(DefaultTheme(), opts, "/tmp/project")
	s.cwd = "~/project"
	return s
}

func TestStatusBarRendersAllFields(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "master", dirty: 3, ahead: 4, ok: true})
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000})
	s.SetTask("running: Read")

	const width = 200
	out := s.Render(width)

	for _, want := range []string{
		"golder",         // app badge
		"claude-opus",    // model
		"high",           // thinking level
		"~/project",      // cwd
		"master",         // git branch
		glyphDirty + "3", // dirty marker
		glyphAhead + "4", // ahead marker
		"42%",            // context usage
		"running: Read",  // task
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got %q", want, out)
		}
	}

	if w := ui.Width(out); w > width {
		t.Errorf("render width %d exceeds terminal width %d", w, width)
	}
}

func TestStatusBarHidesGitWhenNotRepo(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{ok: false})

	out := s.Render(120)
	if strings.Contains(out, "master") || strings.Contains(out, "*") || strings.Contains(out, "+") {
		t.Errorf("git segment should be hidden when ok=false; got %q", out)
	}
}

func TestStatusBarHidesContextWhenUnknown(t *testing.T) {
	s := newTestStatusBar()
	// No telemetry set (window 0) → context segment hidden.
	s.SetTelemetry(telemetryEventView{util: 0.5, window: 0})

	out := s.Render(120)
	if strings.Contains(out, glyphCtx) {
		t.Errorf("context segment should be hidden when window unknown; got %q", out)
	}
}

func TestStatusBarTruncationKeepsPriorityFields(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "master", dirty: 3, ahead: 4, ok: true})
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000})
	s.SetTask("TASK")

	// Narrow width: only the highest-priority fields (task > model > token)
	// should survive; cwd and git should drop first.
	const width = 24
	out := s.Render(width)

	if w := ui.Width(out); w > width {
		t.Fatalf("truncated render width %d exceeds %d: %q", w, width, out)
	}
	if !strings.Contains(out, "TASK") {
		t.Errorf("highest-priority task field dropped under truncation: %q", out)
	}
	// cwd (lowest priority) must be gone before task.
	if strings.Contains(out, "~/project") {
		t.Errorf("lowest-priority cwd should drop first under truncation: %q", out)
	}
}

func TestStatusBarVeryNarrowNeverOverflows(t *testing.T) {
	s := newTestStatusBar()
	s.SetTask("a-fairly-long-task-description-that-cannot-fit")

	for _, width := range []int{1, 2, 3, 5, 8} {
		out := s.Render(width)
		if w := ui.Width(out); w > width {
			t.Errorf("width %d: render width %d overflows: %q", width, w, out)
		}
	}
}

func TestStatusBarZeroWidthEmpty(t *testing.T) {
	s := newTestStatusBar()
	if out := s.Render(0); out != "" {
		t.Errorf("zero width should render empty, got %q", out)
	}
}

// TestStatusBarContextTokenCount checks the context segment shows a
// comma-grouped token count with the percentage once telemetry reports tokens.
func TestStatusBarContextTokenCount(t *testing.T) {
	s := newTestStatusBar()
	s.SetTelemetry(telemetryEventView{util: 0.46, window: 200000, tokens: 90866})

	out := s.Render(200)
	for _, want := range []string{glyphCtx, "90,866", "46%"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got %q", want, out)
		}
	}
}

func TestHumanizeInt(t *testing.T) {
	cases := map[int]string{0: "0", 90866: "90,866", 1000: "1,000", 999: "999", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanizeInt(in); got != want {
			t.Errorf("humanizeInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAbbreviateHome(t *testing.T) {
	home := homeDir()
	if home == "" {
		t.Skip("no home dir available")
	}
	if got := abbreviateHome(home); got != "~" {
		t.Errorf("abbreviateHome(home) = %q, want ~", got)
	}
	if got := abbreviateHome(home + "/foo/bar"); got != "~/foo/bar" {
		t.Errorf("abbreviateHome(home/foo/bar) = %q, want ~/foo/bar", got)
	}
	if got := abbreviateHome("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("abbreviateHome(/etc/passwd) = %q, want unchanged", got)
	}
}

// TestStatusBarGitTextFormatting checks the "*N +N" markers appear only when
// non-zero.
func TestStatusBarGitTextFormatting(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "main", ok: true})
	out := s.Render(120)
	if strings.Contains(out, "*") || strings.Contains(out, "+") {
		t.Errorf("clean tree should show no *N/+N markers: %q", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("branch name missing: %q", out)
	}
}

// barFixture is a bar with every field populated, so a palette test can see all
// seven blocks (or all seven colored fields in plain mode).
func barFixture() statusBar {
	s := newStatusBar(DefaultTheme(), Options{}, "/home/u/work/golder")
	s.SetModel("sonnet-4")
	s.SetThinking("medium")
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000, tokens: 84000})
	s.SetTask("running go test")
	return s
}

// downgrade renders s through colorprofile's writer at profile p — the stage
// where lipgloss v2 rewrites colors for a terminal that cannot show them — and
// returns what the terminal would receive.
func downgrade(t *testing.T, profile colorprofile.Profile, s string) string {
	t.Helper()
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: profile}
	if _, err := fmt.Fprint(w, s); err != nil {
		t.Fatalf("write through %v: %v", profile, err)
	}
	return buf.String()
}

// TestStatusBarPlainModeAt16Colors is the regression for the Linux report
// ("model, thinking level and path all came out light blue and the text was hard
// to read"). The ribbon spends one distinct background per field, and a 16-color
// terminal's palette mapped three of those blocks onto the SAME blue while the
// separator arrows became blue-on-blue — invisible.
//
// The assertion is the property that broke: every field must render with its own
// color. It is checked on the downgraded output, because that is what the terminal
// actually receives.
func TestStatusBarPlainModeAt16Colors(t *testing.T) {
	s := barFixture()
	s.SetColorProfile(colorprofile.ANSI)
	out := downgrade(t, colorprofile.ANSI, s.Render(100))

	// No backgrounds at all: painting one is what the plain form exists to avoid.
	if strings.Contains(out, "48;") || regexp.MustCompile(`\x1b\[4[0-7]m`).MatchString(out) {
		t.Errorf("plain bar must not paint backgrounds:\n%q", out)
	}
	// Each field is present...
	for _, want := range []string{"golder", "sonnet-4", "medium", "/home/u/work/golder", "84,000"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain bar is missing %q:\n%q", want, out)
		}
	}
	// ...and carries its own color: the fields' foreground codes are all distinct,
	// which is exactly what the ribbon's 16-color approximation failed at.
	fgs := regexp.MustCompile(`\x1b\[(?:3[0-79]|9[0-7])m`).FindAllString(out, -1)
	seen := map[string]bool{}
	for _, fg := range fgs {
		seen[fg] = true
	}
	fields := len(s.segments())
	if len(seen) < fields {
		t.Errorf("plain bar rendered %d distinct foregrounds for %d fields; fields must not share a color:\n%q\n%v",
			len(seen), fields, out, fgs)
	}
}

// TestStatusBarKeepsRibbonAt256Colors: a terminal that can show 256 colors gets
// the powerline unchanged, including the filled blocks and their arrows. The
// plain form is a fallback, not a replacement.
func TestStatusBarKeepsRibbonAt256Colors(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		s := barFixture()
		s.SetColorProfile(profile)
		out := downgrade(t, profile, s.Render(100))
		if !strings.Contains(out, "48;5;97") && !strings.Contains(out, "48;5;") {
			t.Errorf("%v should keep the ribbon's filled blocks:\n%q", profile, out)
		}
		if !strings.Contains(out, "\ue0b0") {
			t.Errorf("%v should keep the powerline arrows:\n%q", profile, out)
		}
	}
}

// TestStatusBarProfileSelection pins which profiles get which form: 256 colors is
// the palette the ribbon's constants are written in, so anything below it uses the
// plain form (and 16-color-only terminals, including the 8-color/Ascii fallbacks,
// are never asked to approximate a block palette).
func TestStatusBarProfileSelection(t *testing.T) {
	for _, tc := range []struct {
		profile colorprofile.Profile
		plain   bool
	}{
		{colorprofile.TrueColor, false},
		{colorprofile.ANSI256, false},
		{colorprofile.ANSI, true},
		{colorprofile.Ascii, true},
	} {
		s := barFixture()
		s.SetColorProfile(tc.profile)
		if s.plain != tc.plain {
			t.Errorf("%v: plain = %v, want %v", tc.profile, s.plain, tc.plain)
		}
	}
}

// TestStatusBarPlainModeWidth pins the layout contract for the plain form: the
// rendered row is exactly the requested width (measured in display columns, ANSI
// ignored) and the important fields survive truncation in the same order the
// ribbon uses.
func TestStatusBarPlainModeWidth(t *testing.T) {
	for _, width := range []int{8, 20, 40, 60, 100} {
		s := barFixture()
		s.SetColorProfile(colorprofile.ANSI)
		got := s.Render(width)
		if w := ui.Width(got); w != width {
			t.Errorf("width %d: rendered display width = %d, want exactly %d:\n%q", width, w, width, got)
		}
	}
	// Truncation keeps the same priority order the ribbon uses: the cwd (lowest)
	// goes before the task (highest), so a narrow bar still says what is running.
	narrow := barFixture()
	narrow.SetColorProfile(colorprofile.ANSI)
	got := stripANSI(narrow.Render(30))
	if !strings.Contains(got, "go test") {
		t.Errorf("a 30-column bar should keep the highest-priority field (the task), got %q", got)
	}
	if strings.Contains(got, "/home/u/work") {
		t.Errorf("a 30-column bar should have dropped the cwd (lowest priority), got %q", got)
	}
}

// TestModelColorProfileSelectsBarForm drives the plumbing end to end: Bubble Tea
// detects the terminal's profile and sends ColorProfileMsg, and the model must
// hand it to the bar (a bar that never hears about the profile draws its ribbon on
// a 16-color terminal, which is the bug this closes).
func TestModelColorProfileSelectsBarForm(t *testing.T) {
	base := NewModel(Options{Model: "sonnet-4"})
	if base.statusBar.plain {
		t.Fatal("the initial bar should assume the ribbon (256 colors) until told otherwise")
	}
	for _, tc := range []struct {
		profile colorprofile.Profile
		plain   bool
	}{
		{colorprofile.ANSI, true},
		{colorprofile.ANSI256, false},
		{colorprofile.TrueColor, false},
	} {
		m := apply(t, NewModel(Options{Model: "sonnet-4"}), tea.ColorProfileMsg{Profile: tc.profile})
		if m.statusBar.plain != tc.plain {
			t.Errorf("%v: bar plain = %v, want %v", tc.profile, m.statusBar.plain, tc.plain)
		}
	}
	// The message is handled, not swallowed by the default branch, and it starts
	// no work of its own.
	next, cmd := base.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI})
	if cmd != nil {
		t.Errorf("ColorProfileMsg should not schedule a command, got %v", cmd)
	}
	if !next.(Model).statusBar.plain {
		t.Error("ColorProfileMsg was not applied to the bar")
	}
}
