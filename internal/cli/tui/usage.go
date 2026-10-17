package tui

import (
	"context"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/provider"
)

// usageDoneMsg carries the outcome of a /usage lookup back to the tea loop. The
// lookup talks to the provider's billing endpoint, so it runs on a command
// goroutine and never blocks a frame.
type usageDoneMsg struct {
	text string
	err  error
}

// usageCmd queries the live provider's remaining quota. Every branch returns a
// message: a provider with no endpoint answers with the explanation instead of an
// error, because "this provider cannot be asked" is a normal answer, not a fault.
func (s *runSession) usageCmd(ctx context.Context) tea.Cmd {
	name := s.live.ProviderName
	base := s.live.BaseURL
	creds := s.creds
	return func() tea.Msg {
		if !provider.UsageSupported(name) {
			return usageDoneMsg{text: provider.UsageHint(name)}
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		key := ""
		if creds != nil {
			key = creds.GetAPIKey(ctx, name)
		}
		resolved := ""
		if spec, ok := provider.LookupProviderSpec(name); ok {
			resolved = provider.ResolveBaseURL(spec, base, os.Getenv)
		}
		text, err := provider.FetchUsage(ctx, provider.UsageRequest{
			Provider: name,
			BaseURL:  resolved,
			APIKey:   key,
		})
		if err != nil {
			return usageDoneMsg{err: err}
		}
		return usageDoneMsg{text: text}
	}
}
