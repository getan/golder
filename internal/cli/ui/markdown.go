// This file renders assistant replies as Markdown for interactive terminals.
// The line-oriented REPL streams model text token-by-token, but Markdown can
// only be laid out once the whole block is known (a table or fenced code span
// needs its full extent). So rendering is a turn-end concern: the caller buffers
// the streamed text and calls RenderMarkdown once the assistant turn closes.
//
// Rendering is gated exactly like color (Enabled): only an interactive,
// NO_COLOR-unset stdout gets styled output. Pipes, files, CI, and tests receive
// the raw Markdown source unchanged, so machine consumers and golden tests are
// unaffected. Any renderer failure also falls back to the raw source — pretty
// output is never allowed to lose content.
package ui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
)

// mdRenderer is the lazily-built glamour renderer. Building it parses a style
// and compiles a chroma lexer set, so it is created once and reused across
// turns. A build failure leaves it nil, degrading to raw output.
var (
	mdOnce     sync.Once
	mdRenderer *glamour.TermRenderer
)

// initMarkdown builds the shared renderer on first use. The style selection
// mirrors glamour's auto style (no tty → plain, else dark/light by background)
// but runs the chosen config through WithoutCodeBlockErrorChip, which the
// stock WithAutoStyle option offers no hook for — without it a language-less
// code block that chroma mis-analyses renders as a wall of red.
//
// WithWordWrap(0) disables glamour's hard word-wrap. That matters: with a fixed
// wrap width glamour pads every line with trailing-space background cells out to
// the full column count, so a three-line reply balloons into kilobytes of ANSI
// noise (measured: ~8KB for a short block at width 100 vs. ~0.5KB unwrapped).
// Disabling the wrap lets the terminal soft-wrap long lines itself and keeps the
// rendered output tight — the REPL doesn't track terminal size anyway.
func initMarkdown() {
	mdOnce.Do(func() {
		style := styles.DarkStyleConfig
		if !StdoutIsTerminal() {
			style = styles.NoTTYStyleConfig
		} else if !termenv.HasDarkBackground() {
			style = styles.LightStyleConfig
		}
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(WithoutCodeBlockErrorChip(style)),
			glamour.WithWordWrap(0),
		)
		if err != nil {
			return
		}
		mdRenderer = r
	})
}

// RenderMarkdown returns src rendered as styled terminal Markdown when output
// is an interactive terminal, and src unchanged otherwise. A nil/broken
// renderer or a render error also returns src, so content is never dropped in
// favor of styling. The returned string carries its own trailing newline from
// glamour; callers should not add another.
func RenderMarkdown(src string) string {
	if !Enabled() {
		return src
	}
	if strings.TrimSpace(src) == "" {
		return src
	}
	initMarkdown()
	if mdRenderer == nil {
		return src
	}
	out, err := mdRenderer.Render(src)
	if err != nil {
		return src
	}
	return out
}
