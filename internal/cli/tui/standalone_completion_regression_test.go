package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestStandaloneProTabCompletionAcrossTerminalSizes(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "192.0.2.1 50000 192.0.2.2 22")
	t.Setenv("SSH_TTY", "/dev/pts/1")
	for _, width := range []int{0, 1, 2, 3, 4, 8, 12, 20, 40, 80, 120} {
		for _, height := range []int{0, 1, 3, 8, 24} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				var model tea.Model = NewModel(Options{NoAnimations: true})
				model, _ = model.Update(tea.WindowSizeMsg{Width: width, Height: height})
				for _, r := range "/pro" {
					model, _ = model.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
					_ = model.View()
				}
				m := model.(Model)
				selected, ok := m.menu.current()
				if !ok {
					t.Fatal("/pro did not expose a completion candidate")
				}
				model, command := model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
				m = model.(Model)
				if command != nil || m.quitting || m.running || !strings.HasPrefix(m.input.Value(), "/"+selected.Name+" ") {
					t.Fatalf("Tab executed/quit instead of completing: input=%q quitting=%t running=%t cmd=%v", m.input.Value(), m.quitting, m.running, command != nil)
				}
				_ = model.View()
			})
		}
	}
}
