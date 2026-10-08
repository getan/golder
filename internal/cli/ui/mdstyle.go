package ui

import gansi "github.com/charmbracelet/glamour/ansi"

// WithoutCodeBlockErrorChip returns a copy of cfg whose code-block Error token
// style renders as plain block text instead of glamour's stock red chip.
//
// glamour highlights every code block through chroma. A block with no language
// tag is auto-analysed, and the guess can land on a lexer that reads ordinary
// prose — Chinese annotations in a directory listing, say — as syntax errors,
// marking most of the block Error. The stock style paints Error on a saturated
// red background (#F05B5B dark / #FF5555 light), so a harmless listing renders
// as a wall of red. The tokens stay errors; they just reuse the block's Text
// style, mirroring how the inline-code override works.
//
// CodeBlock.Chroma is a pointer shared with the package-level configs, so the
// Chroma struct is copied before Error is touched: callers can pass
// styles.DarkStyleConfig / LightStyleConfig without mutating the shared style.
func WithoutCodeBlockErrorChip(cfg gansi.StyleConfig) gansi.StyleConfig {
	if cfg.CodeBlock.Chroma == nil {
		return cfg
	}
	chroma := *cfg.CodeBlock.Chroma
	chroma.Error = chroma.Text
	cfg.CodeBlock.Chroma = &chroma
	return cfg
}
