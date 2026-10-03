package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// This file implements the "working" spinner shown while an agent run is in
// flight, mirroring Claude Code's animated status line: a cycling asterisk
// glyph, a whimsical present-progressive verb ("Whirring…"), and a live stats
// readout — elapsed wall-clock time, an estimate of streamed output tokens, and
// the configured thinking effort. It renders on the row just above the input
// while running and disappears when the run ends.

// spinnerTickMsg advances the spinner animation. The model re-issues a tick
// after each frame while a run is in flight and lets the tick lapse once the run
// ends, so the animation stops without a running goroutine.
type spinnerTickMsg time.Time

// spinnerInterval is the frame cadence. ~120ms is brisk enough to read as motion
// without churning the render loop.
const spinnerInterval = 120 * time.Millisecond

// verbRerollFrames re-picks the verb roughly every this many frames (~5s) so a
// long run cycles through several verbs the way Claude Code does.
const verbRerollFrames = 40

// spinnerFrames is the asterisk animation cycled one glyph per tick. The glyphs
// grow from a dim dot to a full star and back, reading as a pulsing sparkle.
var spinnerFrames = []string{"·", "✢", "✳", "∗", "✺", "✻", "✽", "✻", "✺", "∗", "✳", "✢"}

// spinner is the animated working indicator. It is a plain value held by the
// Model: begin() arms it at run start, advance() steps the frame on each tick,
// addTokens() grows the streamed-token estimate, and view() renders the line.
type spinner struct {
	theme    Theme
	running  bool
	frame    int
	verb     string
	start    time.Time
	chars    int    // runes streamed this run (the token estimate divides this)
	thinking string // thinking-effort label, e.g. "medium"; "" hides that stat
	pinned   string // when set, overrides the random verb and stops re-rolling
}

// newSpinner builds an idle spinner bound to the theme.
func newSpinner(theme Theme) spinner {
	return spinner{theme: theme}
}

// begin arms the spinner for a fresh run: it records the start time, picks the
// first verb, resets the frame and token estimate, and stores the thinking-effort
// label to show in the stats.
func (s *spinner) begin(now time.Time, thinking string) {
	s.running = true
	s.frame = 0
	s.start = now
	s.chars = 0
	s.thinking = thinking
	s.verb = randomVerb()
	s.pinned = ""
}

// pin fixes the spinner label to a specific phrase (e.g. "Compacting
// conversation") and stops verb re-rolling until unpin, so a long-running phase
// reads as one steady message rather than cycling words.
func (s *spinner) pin(label string) { s.pinned = label }

// unpin restores the normal cycling verb after a pinned phase ends.
func (s *spinner) unpin() { s.pinned = "" }

// stop parks the spinner when a run ends so view() renders nothing.
func (s *spinner) stop() { s.running = false }

// advance steps the animation one frame and periodically re-rolls the verb so a
// long run does not sit on one word.
func (s *spinner) advance() {
	s.frame++
	if s.pinned == "" && s.frame%verbRerollFrames == 0 {
		s.verb = randomVerb()
	}
}

// addTokens folds a streamed text delta into the running output-token estimate.
// The count is approximate (≈4 chars per token) — enough for a live spinner
// readout, not billing.
func (s *spinner) addTokens(delta string) {
	s.chars += len([]rune(delta))
}

// view renders the spinner line, e.g. "✻ Whirring… (1m 54s · ↓ 242 tokens ·
// medium effort)". It returns "" when not running or before a width is known.
// The glyph and verb take the accent color; the parenthetical stats are dim.
func (s spinner) view(width int) string {
	if !s.running || width <= 0 {
		return ""
	}
	glyph := spinnerFrames[s.frame%len(spinnerFrames)]
	verb := s.verb
	if s.pinned != "" {
		verb = s.pinned
	}
	head := s.theme.Spinner.Render(glyph + " " + verb + "…")

	var stats strings.Builder
	fmt.Fprintf(&stats, "%s", formatElapsed(time.Since(s.start)))
	if tokens := s.chars / 4; tokens > 0 {
		fmt.Fprintf(&stats, " · ↓ %s tokens", humanizeInt(tokens))
	}
	if s.thinking != "" {
		fmt.Fprintf(&stats, " · %s effort", s.thinking)
	}
	line := head + " " + s.theme.System.Render("("+stats.String()+")")
	return TruncateToWidth(line, width)
}

// randomVerb picks one of the built-in present-progressive verbs.
func randomVerb() string {
	return spinnerVerbs[rand.Intn(len(spinnerVerbs))]
}

// formatElapsed renders a duration compactly: "42s", "1m 54s", or "1h 2m".
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs %= 60
	if mins < 60 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	hours := mins / 60
	mins %= 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// spinnerVerbs is Claude Code's 185 built-in spinner verbs (present-progressive
// flavor words shown while working). Sourced from the community catalog at
// github.com/wynandw87/claude-code-spinner-verbs.
var spinnerVerbs = []string{
	"Accomplishing", "Actioning", "Actualizing", "Architecting", "Baking",
	"Beaming", "Beboppin'", "Befuddling", "Billowing", "Blanching",
	"Bloviating", "Boogieing", "Boondoggling", "Booping", "Bootstrapping",
	"Brewing", "Burrowing", "Calculating", "Canoodling", "Caramelizing",
	"Cascading", "Catapulting", "Cerebrating", "Channeling", "Channelling",
	"Choreographing", "Churning", "Clauding", "Coalescing", "Cogitating",
	"Combobulating", "Composing", "Computing", "Concocting", "Considering",
	"Contemplating", "Cooking", "Crafting", "Creating", "Crunching",
	"Crystallizing", "Cultivating", "Deciphering", "Deliberating", "Determining",
	"Dilly-dallying", "Discombobulating", "Doing", "Doodling", "Drizzling",
	"Ebbing", "Effecting", "Elucidating", "Embellishing", "Enchanting",
	"Envisioning", "Evaporating", "Fermenting", "Fiddle-faddling", "Finagling",
	"Flambeing", "Flibbertigibbeting", "Flowing", "Flummoxing", "Fluttering",
	"Forging", "Forming", "Frolicking", "Frosting", "Gallivanting",
	"Galloping", "Garnishing", "Generating", "Germinating", "Gitifying",
	"Grooving", "Gusting", "Harmonizing", "Hashing", "Hatching",
	"Herding", "Honking", "Hullaballooing", "Hyperspacing", "Ideating",
	"Imagining", "Improvising", "Incubating", "Inferring", "Infusing",
	"Ionizing", "Jitterbugging", "Julienning", "Kneading", "Leavening",
	"Levitating", "Lollygagging", "Manifesting", "Marinating", "Meandering",
	"Metamorphosing", "Misting", "Moonwalking", "Moseying", "Mulling",
	"Mustering", "Musing", "Nebulizing", "Nesting", "Newspapering",
	"Noodling", "Nucleating", "Orbiting", "Orchestrating", "Osmosing",
	"Perambulating", "Percolating", "Perusing", "Philosophising", "Photosynthesizing",
	"Pollinating", "Pondering", "Pontificating", "Pouncing", "Precipitating",
	"Prestidigitating", "Processing", "Proofing", "Propagating", "Puttering",
	"Puzzling", "Quantumizing", "Razzle-dazzling", "Razzmatazzing", "Recombobulating",
	"Reticulating", "Roosting", "Ruminating", "Sauteing", "Scampering",
	"Schlepping", "Scurrying", "Seasoning", "Shenaniganing", "Shimmying",
	"Simmering", "Skedaddling", "Sketching", "Slithering", "Smooshing",
	"Sock-hopping", "Spelunking", "Spinning", "Sprouting", "Stewing",
	"Sublimating", "Swirling", "Swooping", "Symbioting", "Synthesizing",
	"Tempering", "Thinking", "Thundering", "Tinkering", "Tomfoolering",
	"Topsy-turvying", "Transfiguring", "Transmuting", "Twisting", "Undulating",
	"Unfurling", "Unravelling", "Vibing", "Waddling", "Wandering",
	"Warping", "Whatchamacalliting", "Whirlpooling", "Whirring", "Whisking",
	"Wibbling", "Working", "Wrangling", "Zesting", "Zigzagging",
}
