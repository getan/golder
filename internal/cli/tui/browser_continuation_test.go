package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browsercontinuation"
	"github.com/getan/golder/internal/browsercontinuation/continuationtest"
	"github.com/getan/golder/internal/permissions"
)

func recoveryModel(t *testing.T) (Model, *continuationtest.Host) {
	t.Helper()
	s := newRemoteTestSession(t)
	tool := agenttool.NewBrowserTool(t.TempDir(), true)
	tools := []agentcore.AgentTool{tool}
	agenttool.ConfigureBrowserSession(tools, s.header.ID)
	m := NewModel(Options{Tools: tools})
	m.session, m.width, m.height = s, 100, 30
	service := agenttool.BrowserContinuations(tools)
	t.Cleanup(service.Close)
	h := continuationtest.New()
	h.Register(service)
	m.browserUI.service, m.browserUI.views = service, service.Snapshot()
	m.relayout()
	return m, h
}

func TestRecoveryCardClickAndKeyboardArePrivateDecisions(t *testing.T) {
	for _, keyboard := range []bool{false, true} {
		m, h := recoveryModel(t)
		m.running = true // Waiting for a browser must not disable its recovery UI.
		if keyboard {
			next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
			m = next.(Model)
			next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = next.(Model)
			if cmd == nil {
				t.Fatal("focused button did not start its private command")
			}
			m.Update(cmd())
		} else {
			y := m.height - 1 - m.input.Height() - len(m.composerExtraLines(m.width)) - len(m.browserCardLines(m.width)) + 1
			next, cmd := m.Update(tea.MouseClickMsg{X: 5, Y: y, Button: tea.MouseLeft})
			m = next.(Model)
			if cmd != nil || h.Calls() != 0 {
				t.Fatal("press dispatched a browser/page action")
			}
			next, cmd = m.Update(tea.MouseReleaseMsg{X: 5, Y: y, Button: tea.MouseLeft})
			m = next.(Model)
			if cmd == nil {
				t.Fatal("button release did not start recovery")
			}
			m.Update(cmd())
		}
		if h.Calls() != 1 {
			t.Fatalf("recovery calls = %d, want 1", h.Calls())
		}
	}
}

func TestRecoveryCardRejectsResizeStaleFocusAndUnshownText(t *testing.T) {
	m, h := recoveryModel(t)
	if _, _, handled := m.browserConfirmation("已授权"); handled {
		t.Fatal("unshown request accepted chat confirmation")
	}
	_ = m.View()
	if _, cmd, handled := m.browserConfirmation("已授权"); !handled || cmd == nil {
		t.Fatal("one displayed request could not be confirmed once")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(Model)
	m.browserUI.views[0].ID = "a-new-request"
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil || h.Calls() != 0 {
		t.Fatal("old keyboard focus selected a replacement request")
	}
	m.browserUI.pressed = "old"
	next, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 15})
	m = next.(Model)
	if m.browserUI.pressed != "" {
		t.Fatal("resize preserved a pending pointer press")
	}
	if id, _ := m.browserCardHit(5, 10); id != "" {
		t.Fatal("truncated button retained an invisible hit region")
	}
	m.session.perms.Set(permissions.ReadOnly)
	if _, cmd := m.continueBrowser("a-new-request", false); cmd != nil {
		t.Fatal("read-only mode issued recovery")
	}
}

func TestRecoveryArrowsAndCompletedDecisionClearCard(t *testing.T) {
	for _, keep := range []bool{false, true} {
		m, h := recoveryModel(t)
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
		if m.browserUI.focus != 1 || m.browserUI.focusID == "" {
			t.Fatal("down did not focus the first choice")
		}
		if keep {
			m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			if m.browserUI.focus != 2 {
				t.Fatal("down did not select keep paused")
			}
			m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
			m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
		}
		next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		if cmd == nil {
			t.Fatal("focused choice did not issue its decision")
		}
		m = apply(t, m, cmd())
		if len(m.browserCardLines(m.width)) != 0 {
			t.Fatalf("completed decision left persistent card: %v", m.browserCardLines(m.width))
		}
		if keep && h.Calls() != 0 {
			t.Fatal("keep paused resumed the browser")
		}
		if keep {
			views := m.browserService().Snapshot()
			m = apply(t, m, browserContinuationPoll{service: m.browserService(), views: views})
			if len(m.browserCardLines(m.width)) != 0 {
				t.Fatal("poll restored dismissed keep-paused card")
			}
			if _, cmd, handled := m.browserConfirmation("/browser continue"); !handled || cmd == nil {
				t.Fatal("kept-paused browser cannot be explicitly resumed later")
			}
		} else if h.Calls() != 1 {
			t.Fatalf("resume was dispatched %d times", h.Calls())
		}
	}
}

func TestRecoveryDoesNotStealArrowKeysFromDraft(t *testing.T) {
	m, _ := recoveryModel(t)
	m.input.SetValue("unfinished")
	next, _, handled := m.browserContinuationEvent(tea.KeyPressMsg{Code: tea.KeyUp})
	if handled || next.(Model).input.Value() != "unfinished" {
		t.Fatal("recovery card intercepted a draft's arrow key")
	}
	if !strings.Contains(strings.Join(m.browserCardLines(m.width), "\n"), "↑↓") {
		t.Fatal("card does not explain arrow navigation")
	}
}

func TestRecoveryReadyOnlyContinuesItsWaitingTaskOnce(t *testing.T) {
	m, _ := recoveryModel(t)
	m.browserUI.waitSession, m.browserUI.waitBrowser, m.browserUI.waitGeneration = m.session.header.ID, "browser-test", 1
	starts := 0
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		if prompt != browserContinuePrompt {
			t.Fatalf("replayed a browser action instead of a fresh observation turn: %q", prompt)
		}
		starts++
		return make(chan tea.Msg), nil
	}
	message := browserContinuationPoll{service: m.browserService(), ready: []browsercontinuation.Ready{{BrowserID: "different", Generation: 1}}}
	next, _ := m.Update(message)
	m = next.(Model)
	if starts != 0 {
		t.Fatal("another browser resumed this task")
	}
	message.ready = []browsercontinuation.Ready{{BrowserID: "browser-test", Generation: 1}}
	next, _ = m.Update(message)
	m = next.(Model)
	m.Update(message)
	if starts != 1 {
		t.Fatalf("same decision started %d turns", starts)
	}
	// Local cancellation and a replacement task revoke continuation intent.
	m, _ = recoveryModel(t)
	m.browserUI.waitSession = m.session.header.ID
	m.browserUI.waitBrowser, m.browserUI.waitGeneration = "browser-test", 1
	next, _ = m.startPrompt("new task")
	m = next.(Model)
	if m.browserUI.waitSession != "" {
		t.Fatal("new task inherited an old recovery intent")
	}
}
