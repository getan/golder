package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browsercontinuation"
	"github.com/getan/golder/internal/permissions"
)

const browserContinuePrompt = "The person resumed this conversation's browser using its local recovery control. Re-observe the current page and continue the previous request under the existing permissions. Do not replay actions whose outcome is unknown."

type browserContinuationUI struct {
	views               []browsercontinuation.View
	service             *browsercontinuation.Coordinator
	waitSession         string
	waitBrowser         string
	waitGeneration      uint64
	ready               bool
	busy, note, pressed string
	pressedKeep         bool
	pressWidth          int
	focus               int
	focusID             string
}

type browserContinuationTick struct{}
type browserContinuationPoll struct {
	service *browsercontinuation.Coordinator
	views   []browsercontinuation.View
	ready   []browsercontinuation.Ready
}
type browserContinuationDone struct {
	service *browsercontinuation.Coordinator
	id      string
	keep    bool
	refresh bool
	err     error
}

func browserContinuationTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return browserContinuationTick{} })
}

func (m Model) browserService() *browsercontinuation.Coordinator {
	if m.session == nil {
		return nil
	}
	return agenttool.BrowserContinuations(m.opts.Tools)
}

func (m Model) pollBrowserContinuations() tea.Cmd {
	s := m.browserService()
	if s == nil {
		return browserContinuationTickCmd()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Poll(ctx)
		return browserContinuationPoll{service: s, views: s.Snapshot(), ready: s.TakeReady()}
	}
}

func (m Model) continueBrowser(id string, keep bool) (tea.Model, tea.Cmd) {
	s := m.browserService()
	if s == nil || m.browserUI.busy != "" {
		return m, nil
	}
	if m.session.perms.Mode() == permissions.ReadOnly {
		m.browserUI.note = "Read-only sessions cannot resume browser automation."
		return m, nil
	}
	refresh := false
	for _, v := range s.Snapshot() {
		if v.ID == id {
			refresh = v.NeedsRefresh
		}
	}
	m.browserUI.busy, m.browserUI.note, m.browserUI.focus = id, "Resuming automation…", 0
	if keep {
		m.browserUI.note = "Keeping browser paused…"
	}
	if refresh && !keep {
		m.browserUI.note = "Refreshing confirmation; this does not resume automation."
	}
	m.relayout()
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var err error
		if keep {
			err = s.KeepPaused(id)
		} else if refresh {
			err = s.Refresh(ctx, id)
		} else {
			err = s.Continue(ctx, id)
		}
		return browserContinuationDone{service: s, id: id, keep: keep, refresh: refresh && !keep, err: err}
	}
}

func (m Model) browserConfirmation(text string) (tea.Model, tea.Cmd, bool) {
	if m.browserInputBlocked() || m.browserService() == nil {
		return m, nil, false
	}
	fields := strings.Fields(text)
	if len(fields) >= 2 && fields[0] == "/browser" {
		if fields[1] != "continue" && fields[1] != "keep-paused" {
			return m, nil, false
		}
		id := ""
		if len(fields) == 3 {
			id = fields[2]
		} else if len(fields) == 2 && len(m.browserUI.views) == 1 {
			id = m.browserUI.views[0].ID
		}
		if id == "" {
			m.transcript.addSystem("Choose the exact browser recovery button or supply its request ID.")
			return m, nil, true
		}
		next, cmd := m.continueBrowser(id, fields[1] == "keep-paused")
		return next, cmd, true
	}
	views := m.browserService().Snapshot()
	if len(views) == 1 && views[0].Displayed && !views[0].KeptPaused && !views[0].NeedsRefresh && !views[0].Busy && !views[0].OutcomeUnknown && browsercontinuation.ConfirmationText(text) {
		next, cmd := m.continueBrowser(views[0].ID, false)
		return next, cmd, true
	}
	if browsercontinuation.ConfirmationText(text) {
		for _, v := range views {
			if v.Displayed {
				m.browserUI.note = "Choose the exact recovery button or refresh its expired request. No browser was resumed."
				m.relayout()
				return m, nil, true
			}
		}
	}
	return m, nil, false
}

func (m Model) browserCardLines(width int) []string {
	if m.browserUI.service == nil || m.browserService() != m.browserUI.service {
		return nil
	}
	var lines []string
	for i, v := range m.browserUI.views {
		header := fmt.Sprintf("Browser %s paused · %s · %s", v.BrowserID, v.Origin, v.Reason)
		if v.Error != "" {
			header = v.Error
		}
		lines = append(lines, TruncateToWidth(header, width))
		label := "[Resume automation]"
		if v.NeedsRefresh {
			label = "[Refresh request]"
		}
		first, second := ' ', ' '
		if m.browserUI.focus == 2*i+1 {
			first = '>'
		}
		if m.browserUI.focus == 2*i+2 {
			second = '>'
		}
		buttons := fmt.Sprintf("%c %-19s %c [Keep paused]", first, label, second)
		if m.browserUI.busy == v.ID || v.Busy {
			buttons = "Resuming automation…"
		} else if !v.Supported {
			buttons = "Upgrade browser host for checked recovery"
		} else if v.OutcomeUnknown {
			buttons = "Outcome unknown; checking receipt, not resending"
		} else if v.RetryAfterMS > 0 {
			buttons = "Server cooldown active; wait for Retry-After"
		}
		lines = append(lines, TruncateToWidth(buttons, width))
	}
	if len(m.browserUI.views) > 0 {
		lines = append(lines, TruncateToWidth("Complete the required step, then click Resume or reply 继续 / 已授权. Tab focuses buttons; /browser continue is the mouse-free alternative.", width))
	}
	if m.browserUI.note != "" {
		lines = append(lines, TruncateToWidth(m.browserUI.note, width))
	}
	return lines
}

func (m Model) browserCardHit(x, y int) (string, bool) {
	lines := m.browserCardLines(m.width)
	if len(lines) == 0 || m.browserInputBlocked() {
		return "", false
	}
	// Cards are immediately above the input and below the popup/search slot.
	base := m.height - 1 - m.input.Height() - len(m.composerExtraLines(m.width)) - len(lines)
	for i, v := range m.browserUI.views {
		if base >= 0 && y == base+2*i+1 && v.Supported && (v.Error == "" || v.NeedsRefresh) && !v.OutcomeUnknown && !v.Busy && v.RetryAfterMS == 0 && m.browserUI.busy == "" {
			if m.width >= 21 && x >= 2 && x < 21 {
				return v.ID, false
			}
			if m.width >= 37 && x >= 24 && x < 37 {
				return v.ID, true
			}
		}
	}
	return "", false
}

func (m Model) browserContinuationEvent(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case browserContinuationTick:
		return m, m.pollBrowserContinuations(), true
	case browserContinuationPoll:
		if msg.service != nil && msg.service == m.browserService() {
			if m.browserUI.service != nil && m.browserUI.service != msg.service {
				m.browserUI = browserContinuationUI{}
			}
			m.browserUI.service, m.browserUI.views = msg.service, msg.views
			for _, ready := range msg.ready {
				if ready.BrowserID == m.browserUI.waitBrowser && ready.Generation == m.browserUI.waitGeneration && m.browserUI.waitSession == msg.service.Session() {
					m.browserUI.ready = true
				}
			}
			if m.browserUI.waitSession == msg.service.Session() && msg.service.ReadyFor(m.browserUI.waitBrowser, m.browserUI.waitGeneration) {
				m.browserUI.ready = true
			}
			m.relayout()
			if m.browserUI.ready && !m.running && !m.browserInputBlocked() && m.session != nil && m.session.perms.Mode() != permissions.ReadOnly && m.browserUI.waitSession == m.session.header.ID {
				m.browserUI.ready, m.browserUI.waitSession = false, ""
				m.transcript.addSystem("Browser automation resumed; re-observing before continuing the previous task.")
				next, cmd := m.startPrompt(browserContinuePrompt)
				return next, tea.Batch(cmd, browserContinuationTickCmd()), true
			}
		}
		return m, browserContinuationTickCmd(), true
	case tea.WindowSizeMsg:
		m.browserUI.pressed, m.browserUI.focusID, m.browserUI.focus = "", "", 0
	case browserContinuationDone:
		if msg.service == m.browserService() {
			m.browserUI.busy = ""
			m.browserUI.note = "Automation resumed."
			if msg.refresh {
				m.browserUI.note = "Confirmation refreshed. Choose Resume automation to continue."
			}
			if msg.keep {
				m.browserUI.note = "Automation remains paused."
			}
			if msg.err != nil {
				m.browserUI.note = "Resume not confirmed: " + msg.err.Error()
			}
			m.relayout()
		}
		return m, nil, true
	case tea.MouseClickMsg:
		if id, keep := m.browserCardHit(msg.X, msg.Y); id != "" && msg.Button == tea.MouseLeft {
			m.browserUI.pressed, m.browserUI.pressedKeep, m.browserUI.pressWidth = id, keep, m.width
			return m, nil, true
		}
	case tea.MouseReleaseMsg:
		if m.browserUI.pressed != "" {
			id, keep := m.browserCardHit(msg.X, msg.Y)
			pressed := m.browserUI.pressed
			m.browserUI.pressed = ""
			if id == pressed && keep == m.browserUI.pressedKeep && m.width == m.browserUI.pressWidth {
				next, cmd := m.continueBrowser(id, keep)
				return next, cmd, true
			}
			return m, nil, true
		}
	case tea.KeyPressMsg:
		if m.browserInputBlocked() || len(m.browserUI.views) == 0 {
			return m, nil, false
		}
		if msg.String() == "tab" && m.input.Value() == "" {
			m.browserUI.focus = (m.browserUI.focus + 1) % (len(m.browserUI.views)*2 + 1)
			if m.browserUI.focus > 0 {
				m.browserUI.focusID = m.browserUI.views[(m.browserUI.focus-1)/2].ID
			}
			return m, nil, true
		}
		if m.input.Value() != "" {
			m.browserUI.focus = 0
		}
		if m.browserUI.focus > 0 && (msg.String() == "enter" || msg.String() == "space") {
			i := (m.browserUI.focus - 1) / 2
			if i < len(m.browserUI.views) && m.browserUI.views[i].ID == m.browserUI.focusID {
				next, cmd := m.continueBrowser(m.browserUI.views[i].ID, m.browserUI.focus%2 == 0)
				return next, cmd, true
			}
			m.browserUI.focus = 0
			return m, nil, true
		}
	}
	return m, nil, false
}

func (m Model) browserInputBlocked() bool {
	return m.approval.active || m.mcpWizard != nil || m.mcpOAuth != nil || m.larkWizard != nil || m.mcpRemoval != nil || m.extensionSetupText != "" || m.resume.pending || m.remoteStarting || m.session != nil && m.session.remote != nil && m.session.remote.pairing
}
