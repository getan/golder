package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/cli/extensioncmd"
)

type extensionSetupProgressMsg struct {
	epoch uint64
	text  string
	next  <-chan tea.Msg
}

func waitExtensionSetup(stream <-chan tea.Msg, epoch uint64) tea.Cmd {
	return func() tea.Msg {
		if msg, ok := <-stream; ok {
			return msg
		}
		return extensionDoneMsg{setupEpoch: epoch, err: errors.New("authorization stopped")}
	}
}

func (m Model) startExtensionSetup(kind string, args []string) (tea.Model, tea.Cmd) {
	m.extensionSetupEpoch++
	epoch := m.extensionSetupEpoch
	ext := m.opts.Extensions
	line := "/" + kind + " " + strings.Join(args, " ")
	return m.startSessionOp(line, "Extension authorization", func(ctx context.Context) tea.Cmd {
		stream := make(chan tea.Msg, 8)
		return func() tea.Msg {
			go func() {
				defer close(stream)
				ctx = extensioncmd.WithSetupDisplay(ctx, func(text string) {
					select {
					case stream <- extensionSetupProgressMsg{epoch: epoch, text: text, next: stream}:
					case <-ctx.Done():
					}
				})
				r, err := extensioncmd.Execute(ctx, ext, kind, args)
				select {
				case stream <- extensionDoneMsg{response: r, err: err, setupEpoch: epoch}:
				case <-time.After(time.Second):
				}
			}()
			return waitExtensionSetup(stream, epoch)()
		}
	})
}
