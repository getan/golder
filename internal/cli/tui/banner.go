package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/provider"
)

// This file builds the startup splash shown at the top of the transcript: the
// animated "GOLDER" wordmark (see logo.go) with the session's basic
// configuration (model, provider, protocol, thinking effort, directory) laid
// out beside it.
// It is seeded by withSession so it scrolls up as the conversation grows, like
// a shell's login banner.

// renderBanner paints the splash in its resting frame; renderBannerFrame is the
// animated variant the model re-renders while the wordmark types itself in.
func renderBanner(theme Theme, opts Options, cwd string) string {
	return renderBannerFrame(theme, opts, cwd, logoFrames)
}

// SSH starts with the composer/status bar, not a block-font splash. Keep the
// original banner for local sessions, including local --no-animations.
func (m *Model) addSessionBanner(frame int) {
	if !m.remoteTerminal {
		m.transcript.addBanner(renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, frame))
	}
}

// renderBannerFrame paints one wordmark frame beside a config panel showing the
// session basics. Updates are offered before the interactive session starts.
func renderBannerFrame(theme Theme, opts Options, cwd string, frame int) string {
	logo := renderLogo(frame)

	title := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Bold(true)
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray))
	value := lipgloss.NewStyle().Foreground(lipgloss.Color(colorUser)).Bold(true)

	rows := [][2]string{
		{"Version", firstNonEmpty(opts.Version, "dev")},
		{"Model", firstNonEmpty(opts.Model, "—")},
		{"Provider", firstNonEmpty(opts.ProviderName, "—")},
		{"Protocol", firstNonEmpty(provider.ProtocolLabel(provider.EffectiveProtocol(opts.ProviderName, opts.Model, opts.Protocol)), "—")},
		{"Thinking", firstNonEmpty(string(opts.ThinkingLevel), "off")},
		{"Directory", firstNonEmpty(cwd, "—")},
	}

	var info strings.Builder
	info.WriteString(title.Render("golder") + "  " + theme.System.Render("Terminal AI coding assistant") + "\n\n")
	for i, r := range rows {
		if i > 0 {
			info.WriteByte('\n')
		}
		info.WriteString(label.Render(fmt.Sprintf("%-10s ", r[0])) + value.Render(r[1]))
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, logo, "   ", info.String())
}

// firstNonEmpty returns s when it is non-empty, otherwise the fallback.
func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
