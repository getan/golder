package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/selfupdate"
)

type updateState struct {
	ctx       context.Context
	ticks     <-chan struct{}
	latest    string
	muted     string
	offer     bool
	checking  bool
	openAfter bool
}

type updateTickMsg struct{}
type updateCheckedMsg struct {
	latest string
	offer  bool
	err    error
}

func (m *Model) startUpdateMonitor() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	disabled := strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_NO_UPDATE_CHECK")))
	if m.opts.NoUpdateCheck || disabled == "1" || disabled == "true" {
		return cancel
	}
	if _, comparable := selfupdate.UpdateAvailable(m.opts.Version, m.opts.Version); !comparable {
		return cancel
	}
	ticks := make(chan struct{}, 1)
	m.updates.ctx, m.updates.ticks = ctx, ticks
	go selfupdate.Monitor(ctx, func() {
		select {
		case ticks <- struct{}{}:
		default:
		}
	})
	return cancel
}

func (m Model) waitUpdateTick() tea.Cmd {
	ctx, ticks := m.updates.ctx, m.updates.ticks
	if ctx == nil || ticks == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			if ctx.Err() == nil {
				return updateTickMsg{}
			}
			return nil
		}
	}
}

func (m *Model) beginUpdateCheck(manual bool) tea.Cmd {
	if m.updates.ctx != nil && m.updates.ctx.Err() != nil {
		return nil
	}
	if manual {
		m.updates.openAfter = true
	}
	if m.updates.checking {
		return nil
	}
	current := m.opts.Version
	if _, comparable := selfupdate.UpdateAvailable(current, current); !comparable {
		m.updates.openAfter = false
		if manual {
			m.transcript.addSystem("Source builds cannot check for updates.")
		}
		return nil
	}
	m.updates.checking = true
	parent := m.updates.ctx
	if parent == nil {
		parent = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		defer cancel()
		latest, err := selfupdate.CheckLatest(ctx, current)
		return updateCheckedMsg{latest: latest, offer: err == nil && selfupdate.StartupOffer(current, latest), err: err}
	}
}

func (m Model) finishUpdateCheck(msg updateCheckedMsg) (tea.Model, tea.Cmd) {
	if !m.updates.checking || m.quitting || (m.updates.ctx != nil && m.updates.ctx.Err() != nil) {
		return m, nil
	}
	manual := m.updates.openAfter
	m.updates.checking, m.updates.openAfter = false, false
	if msg.err != nil {
		if manual {
			m.transcript.addSystem("Update check failed: " + msg.err.Error())
		}
	} else if newer, comparable := selfupdate.UpdateAvailable(m.opts.Version, msg.latest); comparable && newer {
		if oldNewer, _ := selfupdate.UpdateAvailable(msg.latest, m.updates.latest); !oldNewer {
			m.updates.latest, m.updates.offer = msg.latest, msg.offer
		}
		// Only a still-idle, explicitly requested command may open the picker.
		// A result arriving while typing, approving or running only sets the hint.
		if manual && !m.running && !m.approval.active && !m.menu.active && m.histSearch == nil && m.pasteEdit == nil && strings.TrimSpace(m.input.Value()) == "" && m.extensionSetupText == "" && m.mcpWizard == nil && m.mcpOAuth == nil && m.larkWizard == nil {
			return m.openUpdatePicker()
		}
	} else if manual {
		m.transcript.addSystem("No newer stable release is available.")
	}
	m.relayout()
	return m, nil
}

func (m Model) updateHint(width int) string {
	if !m.updates.offer || m.updates.latest == "" {
		return ""
	}
	if m.updates.muted != "" {
		if newer, _ := selfupdate.UpdateAvailable(m.updates.muted, m.updates.latest); !newer {
			return ""
		}
	}
	text := "Update " + m.updates.latest + " available · /update"
	return m.theme.System.Render("  " + TruncateToWidth(text, max(1, width-2)))
}

func registerUpdateCommand(reg *runtime.SlashRegistry) {
	reg.AddBuiltin(runtime.SlashCommand{
		Name: "update", Description: "Check for updates, view upgrade instructions or silence a version",
		ArgumentHint: "[check|info|later|skip]",
		Action:       func(string) string { return "Use golder update outside the TUI to install an update." },
	})
}

func (m Model) runUpdate(line string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/update"))
	m.input.Clear()
	m.menu.close()
	if arg == "check" || (arg == "" && m.updates.latest == "") {
		cmd := m.beginUpdateCheck(true)
		m.relayout()
		return m, cmd
	}
	if arg == "" {
		return m.openUpdatePicker()
	}
	return m.chooseUpdate(arg)
}

func (m Model) openUpdatePicker() (tea.Model, tea.Cmd) {
	m.menu.openPickerDetailed([]pickItem{
		{Title: "View upgrade instructions and release notes", Value: "info", Detail: m.opts.Version + " → " + m.updates.latest},
		{Title: "Do not remind again this session", Value: "later"},
		{Title: "Skip this version", Value: "skip"},
	}, "", "update")
	m.menu.pickHint = "↑↓ select · Enter confirm · Esc close"
	m.relayout()
	return m, nil
}

func (m Model) chooseUpdate(choice string) (tea.Model, tea.Cmd) {
	m.menu.close()
	m.input.Clear()
	if m.updates.latest == "" {
		m.transcript.addSystem("Use /update check to check for a newer release.")
	} else {
		switch choice {
		case "info":
			m.transcript.addSystem(fmt.Sprintf("Golder update: %s → %s\nRelease notes: https://github.com/%s/releases/tag/%s\nAfter finishing your work, exit and run golder update. Homebrew installs: brew upgrade golder. Other package-managed installs: use your installation method. Nothing was installed; this session stays on its current binary.", m.opts.Version, m.updates.latest, selfupdate.Repo, m.updates.latest))
		case "later":
			m.updates.muted = m.updates.latest
		case "skip":
			if err := selfupdate.SkipVersion(m.updates.latest); err != nil {
				m.transcript.addSystem("Could not remember skipped version: " + err.Error())
			} else {
				m.updates.offer = false
				m.updates.muted = m.updates.latest
			}
		default:
			m.transcript.addSystem("usage: /update [check|info|later|skip]")
		}
	}
	m.relayout()
	return m, nil
}
