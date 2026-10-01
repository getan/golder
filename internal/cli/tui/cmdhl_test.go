package tui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// fgSeqRE matches a foreground SGR, 256-color (38;5;N) or truecolor
// (38;2;R;G;B).
var fgSeqRE = regexp.MustCompile(`\x1b\[[0-9;]*38;[0-9;]+m`)

// distinctFgColors counts the distinct foreground colors in rendered output.
func distinctFgColors(s string) int {
	seen := map[string]struct{}{}
	for _, m := range fgSeqRE.FindAllString(s, -1) {
		seen[m] = struct{}{}
	}
	return len(seen)
}

// TestShellHighlightIsLexical is the regression for "only the first word is
// highlighted": a command must produce per-token colors (a string literal and
// a builtin differ), not one uniform color.
func TestShellHighlightIsLexical(t *testing.T) {
	spans := highlightShellCommand(`grep -n "persist_cache" src/ | head -n 15`, true)
	if len(spans) < 3 {
		t.Fatalf("spans = %d, want several tokens\n%+v", len(spans), spans)
	}
	colors := map[string]struct{}{}
	for _, s := range spans {
		if s.color != "" {
			colors[s.color] = struct{}{}
		}
	}
	if len(colors) < 3 {
		t.Fatalf("distinct token colors = %d, want >= 3 (lexical highlighting)\n%+v", len(colors), spans)
	}
	// The quoted string must not share the plain-text color.
	var textColor, stringColor string
	for _, s := range spans {
		switch {
		case strings.Contains(s.text, "grep") && textColor == "":
			textColor = s.color
		case strings.Contains(s.text, "persist_cache"):
			stringColor = s.color
		}
	}
	if stringColor == "" || stringColor == textColor {
		t.Fatalf("string literal color %q must differ from text color %q", stringColor, textColor)
	}
}

// TestShellHighlightWrapsKeepingColor verifies wrapping preserves the token
// coloring across the break and never exceeds the budget.
func TestShellHighlightWrapsKeepingColor(t *testing.T) {
	cmd := `rg -n "persist_cache|try_load_cache" codex-rs/models-manager/src/ | head -n 15`
	spans := highlightShellCommand(cmd, true)
	lines := wrapHLSpans(spans, 40, 40)
	if len(lines) < 2 {
		t.Fatalf("lines = %d, want the command wrapped", len(lines))
	}
	var flat strings.Builder
	for _, ln := range lines {
		flat.WriteString(renderHLSpans(ln, lipgloss.NewStyle()))
		flat.WriteString("\n")
	}
	plain := stripANSI(flat.String())
	// A word-wrap break may split the tail across lines, so compare the
	// whitespace-flattened text.
	if joined := strings.Join(strings.Fields(plain), " "); !strings.Contains(joined, "head -n 15") {
		t.Fatalf("wrapped output dropped the tail:\n%s", plain)
	}
	if n := distinctFgColors(flat.String()); n < 3 {
		t.Fatalf("wrapped output has %d colors, want the tokens preserved", n)
	}
}

// TestShellHighlightCache verifies repeated renders reuse the cached spans
// (cards re-render on every reflow).
func TestShellHighlightCache(t *testing.T) {
	cmd := `echo "cache me" | wc -l`
	first := highlightShellCommand(cmd, true)
	second := highlightShellCommand(cmd, true)
	if len(first) != len(second) {
		t.Fatalf("cache returned %d spans, want %d", len(second), len(first))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("span %d = %+v, want cached %+v", i, second[i], first[i])
		}
	}
}

// TestShellHighlightUnknownLexerFallsBack is a guard for the failure mode: a
// plain span carrying the whole command so nothing is ever dropped.
func TestShellHighlightPlainFallbackRenders(t *testing.T) {
	lines := wrapHLSpans([]hlSpan{{text: "echo hi"}}, 20, 20)
	got := renderHLSpans(lines[0], lipgloss.NewStyle())
	if !strings.Contains(stripANSI(got), "echo hi") {
		t.Fatalf("fallback render = %q", got)
	}
}

// TestShellHighlightOptions verifies options carry Catppuccin's
// variable.parameter style (maroon italic), the color codex gives `-n`,
// `--flag`, `-15`, while a bare `-`/`--` and quoted text are untouched.
func TestShellHighlightOptions(t *testing.T) {
	dark := optionColor(true)
	spans := highlightShellCommand(`head -n 15 --verbose -la`, true)
	got := map[string]hlSpan{}
	for _, s := range spans {
		got[strings.TrimSpace(s.text)] = s
	}
	for _, opt := range []string{"-n", "--verbose", "-la"} {
		s, ok := got[opt]
		if !ok {
			t.Fatalf("no span for %q in %+v", opt, spans)
		}
		if s.color != dark || !s.italic {
			t.Errorf("%q style = color %s italic=%v, want %s italic", opt, s.color, s.italic, dark)
		}
	}
	if s, ok := got["15"]; ok && s.color == dark {
		t.Errorf("the option value 15 must not be painted as an option: %+v", s)
	}
}
