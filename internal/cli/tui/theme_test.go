package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/styles"

	"github.com/getan/golder/internal/cli/ui"
)

func TestDefaultThemeRenders(t *testing.T) {
	th := DefaultTheme()

	cases := map[string]string{
		"user":      th.User.Render("hi"),
		"assistant": th.Assistant.Render("ok"),
		"system":    th.System.Render("note"),
		"toolVerb":  th.ToolVerb.Render("Ran"),
		"toolName":  th.ToolName.Render("bash"),
		"toolCmd":   th.ToolCmd.Render("go test ./..."),
		"toolBody":  th.ToolBody.Render("output"),
		"statusBar": th.StatusBar.Render("status"),
		"accent":    th.Accent.Render("file.go"),
		"error":     th.Error.Render("boom"),
		"warn":      th.Warn.Render("careful"),
		"success":   th.Success.Render("done"),
	}
	for name, got := range cases {
		if got == "" {
			t.Errorf("style %s rendered empty output", name)
		}
	}
}

func TestWrapToWidthDisplayWidth(t *testing.T) {
	// Mix double-width CJK, an emoji, and ASCII.
	const input = "你好world世界🚀测试abc"
	const width = 6

	wrapped := WrapToWidth(input, width)

	// Reassembling the wrapped lines (minus the inserted newlines) must equal
	// the original: nothing is dropped or split inside a rune.
	if got := strings.ReplaceAll(wrapped, "\n", ""); got != input {
		t.Fatalf("wrap altered content: got %q want %q", got, input)
	}

	for _, line := range strings.Split(wrapped, "\n") {
		if w := ui.Width(line); w > width {
			t.Errorf("line %q has display width %d > %d", line, w, width)
		}
		// Guard against a mid-rune cut producing invalid UTF-8.
		if !isValidBoundary(line) {
			t.Errorf("line %q was cut inside a multibyte rune", line)
		}
	}
}

func TestWrapToWidthPreservesNewlines(t *testing.T) {
	out := WrapToWidth("ab\ncd", 10)
	if out != "ab\ncd" {
		t.Fatalf("wrap collapsed existing newlines: got %q", out)
	}
}

func TestWrapToWidthNonPositive(t *testing.T) {
	const s = "你好world"
	if got := WrapToWidth(s, 0); got != s {
		t.Errorf("width<=0 should return input unchanged, got %q", got)
	}
}

// TestWrapToWidthWordsBreaksAtSpaces pins what the wrapper exists for: prose
// wrapped for display breaks between words, never inside one, so a description
// reading "read-only" can never come out as "re" + "ad-only" — the bug the
// rune-based WrapToWidth would produce on the popup's descriptions.
func TestWrapToWidthWordsBreaksAtSpaces(t *testing.T) {
	got := WrapToWidthWords("show or switch the approval mode", 12)
	for _, line := range strings.Split(got, "\n") {
		if w := ui.Width(line); w > 12 {
			t.Errorf("line %q exceeds the width (%d > 12)", line, w)
		}
	}
	// Every line but a hard-broken tail must end on a word, so the reassembled
	// text contains no word that the original did not.
	if joined := strings.ReplaceAll(got, "\n", " "); joined != "show or switch the approval mode" {
		t.Errorf("word wrap altered the text: %q", joined)
	}
	if !strings.Contains(got, "approval") {
		t.Errorf("a word that fits must stay whole: %q", got)
	}
	// The packing is greedy, not merely legal: no line could have taken the first
	// word of the line after it, or the text would have come out longer than the
	// width required — the property that keeps the popup compact instead of
	// wrapping at every space.
	lines := strings.Split(got, "\n")
	for i := 0; i+1 < len(lines); i++ {
		next := strings.SplitN(lines[i+1], " ", 2)[0]
		if w := ui.Width(lines[i]) + 1 + ui.Width(next); w <= 12 {
			t.Errorf("line %d (%q) could have taken %q (would be %d columns):\n%s", i, lines[i], next, w, got)
		}
	}
}

// TestWrapToWidthWordsHardBreaksLongTokens covers the fallback: a token with no
// spaces to break at (a usage string, a path, a CJK run) still fits the width by
// breaking by rune, so it can never overflow the popup.
func TestWrapToWidthWordsHardBreaksLongTokens(t *testing.T) {
	const long = "[read-only|ask|auto|full-access|writable|readable]"
	for _, line := range strings.Split(WrapToWidthWords(long, 20), "\n") {
		if w := ui.Width(line); w > 20 {
			t.Errorf("unbreakable token overflowed: %q (%d > 20)", line, w)
		}
	}
	// The break lands on the token's own separator rather than inside a word:
	// every line but the last ends at a "|", so "readable" is never split into
	// "rea" + "dable" the way a rune wrapper would leave it.
	lines := strings.Split(WrapToWidthWords(long, 20), "\n")
	for i, line := range lines[:len(lines)-1] {
		if !strings.HasSuffix(line, "|") {
			t.Errorf("line %d should have broken at a separator: %q (all: %q)", i, line, lines)
		}
	}
	// The last piece survives intact — the packing stays greedy, so it may share
	// its line with the piece before it.
	if tail := lines[len(lines)-1]; !strings.HasSuffix(tail, "readable]") || strings.Contains(tail, "readab\n") {
		t.Errorf("the last piece should stay whole, got %q", tail)
	}
	// CJK has no spaces at all, so it is the same path.
	const cjk = "这是一个没有空格的描述文本"
	for _, line := range strings.Split(WrapToWidthWords(cjk, 8), "\n") {
		if w := ui.Width(line); w > 8 {
			t.Errorf("CJK line overflowed: %q (%d > 8)", line, w)
		}
	}
	// Newlines stay hard breaks, and a non-positive width is a no-op, exactly as
	// in WrapToWidth.
	if got := WrapToWidthWords("ab\ncd", 10); got != "ab\ncd" {
		t.Errorf("word wrap collapsed a hard newline: %q", got)
	}
	if got := WrapToWidthWords("keep me", 0); got != "keep me" {
		t.Errorf("width<=0 should return the input unchanged, got %q", got)
	}
}

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
	}{
		{"cjk", "你好世界测试内容很长", 6},
		{"emoji", "🚀🚀🚀🚀🚀🚀", 5},
		{"mixed", "abc你好def世界🚀tail", 8},
		{"ascii", "helloworld", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateToWidth(tt.in, tt.width)
			if w := ui.Width(got); w > tt.width {
				t.Errorf("truncated %q to display width %d > %d (result %q)", tt.in, w, tt.width, got)
			}
			if !isValidBoundary(got) {
				t.Errorf("truncation cut inside a multibyte rune: %q", got)
			}
			// It must actually be a truncation: contain the ellipsis when the
			// input was wider than the budget.
			if ui.Width(tt.in) > tt.width && !strings.Contains(got, ellipsis) {
				t.Errorf("expected ellipsis in truncated result, got %q", got)
			}
		})
	}
}

func TestTruncateToWidthNoTruncationNeeded(t *testing.T) {
	const s = "你好"
	if got := TruncateToWidth(s, 10); got != s {
		t.Errorf("short string should be returned unchanged, got %q", got)
	}
}

func TestTruncateToWidthNonPositive(t *testing.T) {
	if got := TruncateToWidth("你好", 0); got != "" {
		t.Errorf("width<=0 should return empty string, got %q", got)
	}
}

// isValidBoundary reports whether s contains no invalid UTF-8, which would be
// the tell-tale of a cut inside a multibyte rune.
func isValidBoundary(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// TestMarkdownStyleInlineCodeNotRed is the palette regression: the stock
// glamour dark style paints inline code 203 (coral red) on a chip, which turned
// identifier-dense replies into a wall of red. golder overrides it to cyan with
// no chip, matching codex.
func TestMarkdownStyleInlineCodeNotRed(t *testing.T) {
	for _, dark := range []bool{true, false} {
		cfg := markdownStyle(dark)
		if cfg.Code.Color == nil {
			t.Fatalf("dark=%v: inline code color unset", dark)
		}
		if got := *cfg.Code.Color; got == "203" {
			t.Errorf("dark=%v: inline code still coral red (203)", dark)
		}
		if cfg.Code.BackgroundColor != nil {
			t.Errorf("dark=%v: inline code still carries a chip background", dark)
		}
		if cfg.LinkText.Color == nil || *cfg.LinkText.Color != *cfg.Code.Color {
			t.Errorf("dark=%v: link text should share the code color", dark)
		}
	}
	// The tweak must not mutate the shared package style.
	if styles.DarkStyleConfig.Code.Color == nil || *styles.DarkStyleConfig.Code.Color != "203" {
		t.Fatal("shared glamour dark style was mutated")
	}
}

// TestMarkdownStyleCodeBlockErrorNoChip pins the other half of the palette
// fix: an auto-analysed code block can carry chroma Error tokens, which the
// stock style paints on a red chip. Both palettes must render them as plain
// block text, and the shared glamour configs must stay untouched.
func TestMarkdownStyleCodeBlockErrorNoChip(t *testing.T) {
	for _, dark := range []bool{true, false} {
		cfg := markdownStyle(dark)
		if cfg.CodeBlock.Chroma == nil {
			t.Fatalf("dark=%v: code block chroma missing", dark)
		}
		if bg := cfg.CodeBlock.Chroma.Error.BackgroundColor; bg != nil {
			t.Errorf("dark=%v: code block Error still carries a chip background %q", dark, *bg)
		}
		if cfg.CodeBlock.Chroma.Error.Color == nil {
			t.Errorf("dark=%v: code block Error should reuse the block text color", dark)
		}
	}
	if styles.DarkStyleConfig.CodeBlock.Chroma == nil ||
		styles.DarkStyleConfig.CodeBlock.Chroma.Error.BackgroundColor == nil ||
		*styles.DarkStyleConfig.CodeBlock.Chroma.Error.BackgroundColor != "#F05B5B" {
		t.Fatal("shared glamour dark style was mutated")
	}
	if styles.LightStyleConfig.CodeBlock.Chroma == nil ||
		styles.LightStyleConfig.CodeBlock.Chroma.Error.BackgroundColor == nil ||
		*styles.LightStyleConfig.CodeBlock.Chroma.Error.BackgroundColor != "#FF5555" {
		t.Fatal("shared glamour light style was mutated")
	}
}

// TestCodeBlockErrorRendersWithoutChip is the behavioral half: the Go lexer
// emits Error tokens for a stray "@" sequence, so this input deterministically
// exercises the Error style through the real transcript renderer. Before the
// fix the stock style painted it with a 48;5;203 background.
func TestCodeBlockErrorRendersWithoutChip(t *testing.T) {
	r := rendererFor(80)
	if r == nil {
		t.Fatal("nil markdown renderer")
	}
	out, err := r.Render("```go\n@@@\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@@@") {
		t.Fatalf("code block content missing from render: %q", out)
	}
	for _, bg := range []string{"48;5;", "48;2;"} {
		if i := strings.Index(out, bg); i >= 0 {
			lo := i - 40
			if lo < 0 {
				lo = 0
			}
			hi := i + 40
			if hi > len(out) {
				hi = len(out)
			}
			t.Fatalf("Error token painted a background chip (%s): %q", bg, out[lo:hi])
		}
	}
}
