package ui

import (
	"testing"

	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
)

// TestWithoutCodeBlockErrorChip covers the palette fix: the returned config
// renders chroma Error tokens as plain block text on both stock palettes.
func TestWithoutCodeBlockErrorChip(t *testing.T) {
	assertChipCleared := func(t *testing.T, cfg gansi.StyleConfig) {
		t.Helper()
		if cfg.CodeBlock.Chroma == nil {
			t.Fatal("code block chroma missing")
		}
		ch := cfg.CodeBlock.Chroma
		if ch.Error.BackgroundColor != nil {
			t.Errorf("Error still carries a chip background %q", *ch.Error.BackgroundColor)
		}
		if ch.Error.Color == nil || ch.Text.Color == nil || *ch.Error.Color != *ch.Text.Color {
			t.Error("Error should reuse the block text color")
		}
	}

	assertChipCleared(t, WithoutCodeBlockErrorChip(styles.DarkStyleConfig))
	assertChipCleared(t, WithoutCodeBlockErrorChip(styles.LightStyleConfig))

	// The helper must not mutate the shared package-level configs.
	if bg := styles.DarkStyleConfig.CodeBlock.Chroma.Error.BackgroundColor; bg == nil || *bg != "#F05B5B" {
		t.Fatal("shared glamour dark style was mutated")
	}
	if bg := styles.LightStyleConfig.CodeBlock.Chroma.Error.BackgroundColor; bg == nil || *bg != "#FF5555" {
		t.Fatal("shared glamour light style was mutated")
	}
}

// A config without chroma (e.g. the no-tty style) passes through unchanged.
func TestWithoutCodeBlockErrorChipNilChroma(t *testing.T) {
	cfg := WithoutCodeBlockErrorChip(gansi.StyleConfig{})
	if cfg.CodeBlock.Chroma != nil {
		t.Fatal("nil chroma should pass through untouched")
	}
}
