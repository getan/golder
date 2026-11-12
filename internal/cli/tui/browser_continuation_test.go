package tui

import (
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
