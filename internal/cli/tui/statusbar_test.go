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
	opts := Options{Model: "claude-opus", ProviderName: "openai", ThinkingLevel: agentcore.ThinkingHigh}
	s := newStatusBar(DefaultTheme(), opts, "/tmp/project")
	s.cwd = "~/project"
	return s
}

// focusSegment returns s with the segment whose text starts with glyph pinned
// open, which is the state a click puts the bar in. Tests use it to assert a
// field's whole value under the fold.
func focusSegment(t *testing.T, s statusBar, glyph string) statusBar {
	t.Helper()
	for i, seg := range s.segments() {
		if strings.HasPrefix(seg.text, glyph) {
			s.pin = focusIndex(i)
			return s
		}
	}
	t.Fatalf("no segment starting with %q", glyph)
	return s
}

func TestStatusBarRendersAllFields(t *testing.T) {
	s := newTestStatusBar()
	s.SetGit(gitInfoMsg{branch: "master", dirty: 3, ahead: 4, ok: true})
	s.SetTelemetry(telemetryEventView{util: 0.42, window: 200000})
	s.SetTask("running: Read")
	s.SetMode("auto")

	const width = 200
	out := stripANSI(s.Render(width))

	// The lead — provider and branch — is always whole.
	for _, want := range []string{
		"openai",         // provider
		"master",         // git branch
		glyphDirty + "3", // dirty marker
		glyphAhead + "4", // ahead marker
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got %q", want, out)
		}
	}

	if w := ui.Width(out); w > width {
		t.Errorf("render width %d exceeds terminal width %d", w, width)
	}

	// Every other field is one click away, and shows its whole value when pinned.
	for _, tc := range []struct{ glyph, want string }{
		{glyphMode, "auto"},
		{glyphModel, "claude-opus"},
		{glyphThink, "high"},
		{glyphCwd, "~/project"},
		{glyphCtx, "42%"},
		{glyphTask, "running: Read"},
	} {
		focused := stripANSI(focusSegment(t, s, tc.glyph).Render(width))
		if !strings.Contains(focused, tc.want) {
			t.Errorf("focused %q should show %q; got %q", tc.glyph, tc.want, focused)
		}
	}
}

// TestStatusBarProviderLeadsTheBar pins the left-most block: it holds the
// provider name, and the retired app badge ("golder") is gone — the word is on
// every line of the transcript already, while the gateway a conversation talks
// to was the one fact the bar could not express. An unknown provider renders no
// block at all rather than a placeholder.
func TestStatusBarProviderLeadsTheBar(t *testing.T) {
	s := newTestStatusBar()
	segs := s.segments()
	if len(segs) == 0 || segs[0].text != "openai" {
		t.Fatalf("leading segment = %+v, want the provider block", segs)
	}
	if out := stripANSI(s.Render(120)); strings.Contains(out, "golder") {
		t.Errorf("the app badge is retired; the leading block belongs to the provider: %q", out)
	}

	s.SetProvider("")
	if segs := s.segments(); len(segs) > 0 && (segs[0].text == "openai" || segs[0].text == "golder") {
		t.Errorf("unknown provider should add no block, got leading %+v", segs[0])
	}
}

// TestSyncLiveDisplayPublishesLiveConfig exercises the helper every live-state
// command ends with, independently of the command that moved the config: what
// the bar shows must be live's provider/model/thinking, not the launch snapshot
// it was constructed with.
func TestSyncLiveDisplayPublishesLiveConfig(t *testing.T) {
	m := NewModel(Options{Model: "launch-model", ProviderName: "launch-provider"})
	if m.statusBar.provider != "launch-provider" || m.statusBar.model != "launch-model" {
		t.Fatalf("initial bar = (%q, %q), want the launch config", m.statusBar.provider, m.statusBar.model)
	}
	m.live.ProviderName = "openai"
	m.live.Model = "gpt-4o"
	m.live.ThinkingLevel = agentcore.ThinkingLow
	m.syncLiveDisplay()

	if m.statusBar.provider != "openai" || m.statusBar.model != "gpt-4o" {
		t.Errorf("bar = (%q, %q), want the live (openai, gpt-4o)", m.statusBar.provider, m.statusBar.model)
	}
	if m.statusBar.thinking != "low" {
		t.Errorf("thinking = %q, want the live level", m.statusBar.thinking)
	}
}

// TestStatusBarShowsAShortPathBeforeScrolling: a roomy terminal gets a still bar.
// When the whole path does not fit but its directory name does, the bar spends the
// abbreviation — "…/golder" — and keeps everything visible without moving, which
// is what a normal-sized window should see.
func TestStatusBarShowsAShortPathBeforeScrolling(t *testing.T) {
	s := newStatusBar(DefaultTheme(), Options{
		Model:         "claude-haiku-5-5",
		ProviderName:  "opencode-go",
		ThinkingLevel: agentcore.ThinkingHigh,
	}, "/tmp/x")
	s.cwd = "~/work/harness/golder"
	s.SetGit(gitInfoMsg{branch: "master", ok: true})
	// The path is what this test is about, so it is the focused field: a folded
	// field has nothing to shorten.
	s = focusSegment(t, s, glyphCwd)

	// Below the path's natural width the focused field is elided; a focused field
	// keeps its whole value for as long as the row can afford it, so the
	// abbreviation appears only at a width that truly cannot hold it. At this
	// width the path is the one field left, which is the point of the focus: the
	// directory stays readable even when the rest has to go.
	const narrow = 20
	out := stripANSI(s.Render(narrow))
	if !strings.Contains(out, "…/golder") {
		t.Errorf("a %d-column bar should shorten the path, got %q", narrow, out)
	}
	if strings.Contains(out, "~/work/harness/golder") {
		t.Errorf("the full path should not fit at %d columns: %q", narrow, out)
	}
}

// TestStatusBarWindowsLongFields: the focused field keeps its whole value while
// the row can afford it, and is elided only when the row truly cannot. With the
// fold, the roomy case shows the full path while the model stays a readable tail;
// the phone-sized case still names the directory.
func TestStatusBarWindowsLongFields(t *testing.T) {
	s := newStatusBar(DefaultTheme(), Options{
		Model:         "claude-haiku-5-5",
		ProviderName:  "opencode-go",
		ThinkingLevel: agentcore.ThinkingHigh,
	}, "/tmp/x")
	s.cwd = "~/work/harness/golder"
	s.SetGit(gitInfoMsg{branch: "master", ok: true})

	// The path is the field this test windows, so it holds the focus: a folded
	// field has no window to scroll.
	s = focusSegment(t, s, glyphCwd)

	const roomy = 80
	wide := stripANSI(s.Render(roomy))
	if !strings.Contains(wide, "~/work/harness/golder") {
		t.Errorf("a %d-column bar should show the focused path whole: %q", roomy, wide)
	}
	if strings.Contains(wide, "claude-haiku-5-5") {
		t.Errorf("the model should stay folded while the path is focused: %q", wide)
	}
	if w := ui.Width(wide); w != roomy {
		t.Errorf("row width = %d, want exactly %d: %q", w, roomy, wide)
	}
	s.StartReveal(roomy)
	if s.revealLeft != 0 {
		t.Errorf("a focused field that fits should not reveal, got %d steps", s.revealLeft)
	}

	const narrow = 20
	s.StartReveal(narrow)
	if s.revealLeft <= 0 {
		t.Error("an elided bar should reveal its values once when they change")
	}
	// The reveal is a pass, not a state: clear it so the frames below are the
	// settled ones.
	s.revealLeft, s.revealMax, s.scroll = 0, 0, 0

	out := stripANSI(s.Render(narrow))
	if !strings.Contains(out, "…/golder") {
		t.Errorf("the elided path should still name the directory: %q", out)
	}
	for _, frame := range []string{out, stripANSI(s.Render(narrow))} {
		if w := ui.Width(frame); w != narrow {
			t.Errorf("row width = %d, want exactly %d:\n%q", w, narrow, frame)
		}
	}

	// The bar is still: the same width renders the same row, frame after frame.
	if again := stripANSI(s.Render(narrow)); again != out {
		t.Errorf("the bar moved on its own:\n%q\n%q", out, again)
	}

	// A bar that fits the focused field has nothing to reveal at all.
	s.StartReveal(200)
	if s.revealLeft != 0 {
		t.Error("a bar that fits should reveal nothing")
	}
	if wide := stripANSI(s.Render(200)); !strings.Contains(wide, "~/work/harness/golder") {
		t.Errorf("a wide bar should show the whole path:\n%q", wide)
	}
}

// TestSlideWindowClampsAtBothEnds pins the reveal's window: it slides from the
// head to the tail and stops at either end rather than wrapping.
func TestSlideWindowClampsAtBothEnds(t *testing.T) {
	const text = "abcdef"
	cases := []struct {
		off, width int
		want       string
	}{
		{0, 6, "abcdef"},
		{0, 3, "abc"},
		{2, 3, "cde"},
		{3, 3, "def"},
		{99, 3, "def"}, // clamped at the tail
		{-4, 3, "abc"}, // clamped at the head
	}
	for _, tc := range cases {
		if got := slideWindow(text, tc.width, tc.off); got != tc.want {
			t.Errorf("slideWindow(%q, %d, off=%d) = %q, want %q", text, tc.width, tc.off, got, tc.want)
		}
	}
}

// TestStatusBarRevealRunsOnceAndSettles: a value that just changed is shown in
// motion for one pass — head to tail — and then settles onto the still, elided
// form. The window it slides in is exactly as wide as the settled form, so the
// fields around it never move.
func TestStatusBarRevealRunsOnceAndSettles(t *testing.T) {
	s := newStatusBar(DefaultTheme(), Options{
		Model:         "claude-haiku-5-5",
		ProviderName:  "opencode-go",
		ThinkingLevel: agentcore.ThinkingHigh,
	}, "/tmp/x")
	s.cwd = "~/work/harness/golder"
	s.SetGit(gitInfoMsg{branch: "master", ok: true})
	// Only the focused field can be elided, so the path takes the focus.
	s = focusSegment(t, s, glyphCwd)

	const narrow = 20
	settled := stripANSI(s.Render(narrow))
	s.StartReveal(narrow)
	if s.revealLeft <= 0 {
		t.Fatal("a bar with elided fields should reveal them once")
	}
	first := stripANSI(s.Render(narrow))
	if ui.Width(first) != narrow || ui.Width(settled) != narrow {
		t.Fatalf("row width = %d revealing, %d settled, want %d", ui.Width(first), ui.Width(settled), narrow)
	}
	if first == settled {
		t.Errorf("the first revealing frame should differ from the settled one:\n%q\n%q", first, settled)
	}

	for i := 0; i < 200 && s.revealLeft > 0; i++ {
		s.AdvanceReveal()
	}
	if s.revealLeft != 0 {
		t.Fatalf("the reveal did not finish: %d steps left", s.revealLeft)
	}
	if got := stripANSI(s.Render(narrow)); got != settled {
		t.Errorf("after the pass the bar should be still again:\n%q\n%q", got, settled)
	}
}

// TestModelRevealRunsOnceThenStops: the pass is bounded. A resize that leaves a
// value elided arms it, each tick advances it and re-arms itself, and once it is
// spent the bar schedules nothing more — while a width that fits everything arms
// nothing at all.
func TestModelRevealRunsOnceThenStops(t *testing.T) {
	clearMotionEnv(t)
	m := NewModel(Options{Model: "claude-haiku-5-5", ProviderName: "opencode-go"})
	m.statusBar.cwd = "~/work/harness/golder"
	// Only a focused field is animated; focus the path so the narrow layout has
	// something to slide.
	m.statusBar.pin = focusIndex(len(m.statusBar.segments()) - 1) // the path is last in this fixture
	for i, seg := range m.statusBar.segments() {
		if strings.HasPrefix(seg.text, glyphCwd) {
			m.statusBar.pin = focusIndex(i)
		}
	}

	got, cmd := m.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	m = got.(Model)
	if m.statusBar.revealLeft <= 0 {
		t.Fatal("expected a reveal at 20 columns")
	}
	if cmd == nil {
		t.Fatal("an elided bar should arm the reveal")
	}

	steps := 0
	for cmd != nil && steps < 300 {
		got, cmd = m.Update(statusRevealMsg{})
		m = got.(Model)
		steps++
	}
	if m.statusBar.revealLeft != 0 {
		t.Errorf("the reveal never finished (%d steps)", steps)
	}
	if cmd != nil {
		t.Error("a spent reveal should schedule nothing")
	}

	// A width that fits everything arms nothing.
	got, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	wider := got.(Model)
	if wider.statusBar.revealLeft != 0 {
		t.Errorf("a bar that fits should not reveal anything: %d steps left", wider.statusBar.revealLeft)
	}
	if _, cmd := wider.Update(statusRevealMsg{}); cmd != nil {
		t.Error("no tick should survive a bar that fits")
	}
}

// TestStatusBarShowsApprovalMode: the bar carries the live approval mode, so
// "will a human be asked?" is answerable without opening a dialog or remembering
// what was typed at /permissions.
func TestStatusBarShowsApprovalMode(t *testing.T) {
	s := newTestStatusBar()
	s.SetMode("auto")
	if out := stripANSI(s.Render(120)); !strings.Contains(out, glyphMode+" auto") {
		t.Errorf("mode segment missing: %q", out)
	}
	s.SetMode("full-access")
	if out := stripANSI(s.Render(120)); !strings.Contains(out, "full-access") {
		t.Errorf("mode segment should follow the switch: %q", out)
	}
	s.SetMode("")
	if out := stripANSI(s.Render(120)); strings.Contains(out, glyphMode) {
		t.Errorf("empty mode should render no segment: %q", out)
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

	// The fold changes what fits, not who yields: the SPEC priority order still
	// decides, so the cwd (lowest) goes before the task (highest) and a narrow bar
	// still says what is running.
	const width = 40
	out := stripANSI(s.Render(width))

	if w := ui.Width(out); w != width {
		t.Fatalf("render width %d, want exactly %d: %q", w, width, out)
	}
	if !strings.Contains(out, glyphTask) {
		t.Errorf("highest-priority task field dropped under truncation: %q", out)
	}
	if strings.Contains(out, glyphCwd) {
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
	s = focusSegment(t, s, glyphCtx)

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
	s.SetProvider("openai")
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
	// Each field is reachable, and the folded ones show their value when focused...
	for _, tc := range []struct{ glyph, want string }{
		{glyphThink, "medium"},
		{glyphCwd, "/home/u/work/golder"},
		{glyphCtx, "84,000"},
		{glyphTask, "go test"},
	} {
		focused := downgrade(t, colorprofile.ANSI, focusSegment(t, s, tc.glyph).Render(100))
		if !strings.Contains(focused, tc.want) {
			t.Errorf("focused plain field %q is missing %q:\n%q", tc.glyph, tc.want, focused)
		}
	}
	// ...and the lead is whole without any focus at all.
	for _, want := range []string{"openai", "sonnet-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain bar is missing the lead %q:\n%q", want, out)
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
	if !strings.Contains(got, glyphTask) {
		t.Errorf("a 30-column bar should keep the highest-priority field (the task), got %q", got)
	}
	if strings.Contains(got, glyphCwd) {
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
