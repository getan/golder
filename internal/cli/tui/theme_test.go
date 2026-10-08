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
