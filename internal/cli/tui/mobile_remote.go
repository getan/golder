package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/remotecontrol"
)

type mobileTUIAction struct {
	ctx                            context.Context
	kind, text, approvalID, choice string
	reply                          chan error
}

type mobileTUI struct {
	mu        sync.Mutex
	sessionID string
	snapshot  remotecontrol.MobileSnapshot
	private   bool
	actions   chan mobileTUIAction
	done      chan struct{}
	once      sync.Once
}

func newMobileTUI(snapshot remotecontrol.MobileSnapshot) *mobileTUI {
	return &mobileTUI{sessionID: snapshot.SessionID, snapshot: snapshot, actions: make(chan mobileTUIAction, 8), done: make(chan struct{})}
}

func (a *mobileTUI) Done() <-chan struct{} { return a.done }

func (a *mobileTUI) close() { a.once.Do(func() { close(a.done) }) }

func (a *mobileTUI) Snapshot() (remotecontrol.MobileSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.private || a.snapshot.SessionID != a.sessionID {
		return remotecontrol.MobileSnapshot{}, errors.New("session changed or private operation in progress")
	}
	out := a.snapshot
	out.Messages = append([]remotecontrol.MobileMessage(nil), a.snapshot.Messages...)
	out.Approvals = append([]remotecontrol.MobileApproval(nil), a.snapshot.Approvals...)
	return out, nil
}

func (a *mobileTUI) call(ctx context.Context, action mobileTUIAction) error {
	action.ctx = ctx
	action.reply = make(chan error, 1)
	select {
	case a.actions <- action:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return errors.New("remote session stopped")
	}
	select {
	case err := <-action.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return errors.New("remote session stopped")
	}
}

func (a *mobileTUI) Submit(ctx context.Context, text string) error {
	return a.call(ctx, mobileTUIAction{kind: "input", text: text})
}

func (a *mobileTUI) Answer(ctx context.Context, id, choice string) error {
	return a.call(ctx, mobileTUIAction{kind: "answer", approvalID: id, choice: choice})
}

func (a *mobileTUI) Disconnected() {
	select {
	case a.actions <- mobileTUIAction{kind: "disconnect"}:
	case <-a.done:
	}
}

type mobileStartMsg struct {
	owner   *runSession
	adapter *mobileTUI
	server  *remotecontrol.SecureServer
	closeQR func()
	ready   chan struct{}
	seq     uint64
	err     error
}
type mobilePairMsg struct {
	server  *remotecontrol.SecureServer
	request remotecontrol.PairRequest
}
type mobileStoppedMsg struct{ server *remotecontrol.SecureServer }
type mobileActionMsg struct {
	adapter *mobileTUI
	action  mobileTUIAction
}

func (m Model) mobileSnapshot() (remotecontrol.MobileSnapshot, bool) {
	if m.session == nil {
		return remotecontrol.MobileSnapshot{}, true
	}
	out := remotecontrol.MobileSnapshot{SessionID: m.session.header.ID, Frontend: "TUI", Project: m.cwd, Title: m.session.header.DisplayName, Permission: m.session.perms.Mode().String(), Running: m.running}
	start := max(len(m.transcript.blocks)-200, 0)
	out.Truncated = start > 0
	for i := start; i < len(m.transcript.blocks); i++ {
		block := m.transcript.blocks[i]
		item := remotecontrol.MobileMessage{ID: fmt.Sprintf("%d", i)}
		switch block.role {
		case roleUser:
			if strings.HasPrefix(strings.TrimSpace(block.text), "/remote-control") {
				continue
			}
			item.Role, item.Text = "user", block.text
		case roleAssistant:
			item.Role, item.Text = "assistant", block.text
		case roleTool:
			if block.card == nil {
				continue
			}
			item.Role, item.Text = "tool", block.card.name
		default:
			continue
		}
		out.Messages = append(out.Messages, item)
	}
	if m.approval.active && m.approval.remoteID != "" {
		view := remotecontrol.ProjectApproval(m.approval.remoteID, m.approval.remoteReq)
		view.ToolCallID = m.approval.remoteCallID
		out.Approvals = append(out.Approvals, view)
	}
	private := m.mcpWizard != nil || m.mcpOAuth != nil || m.larkWizard != nil || m.mcpRemoval != nil || m.extensionSetupText != ""
	return out, private
}

func (m Model) publishMobileSnapshot() {
	if m.session == nil || m.session.remote == nil || m.session.remote.mobile == nil {
		return
	}
	adapter := m.session.remote.mobile
	snapshot, private := m.mobileSnapshot()
	adapter.mu.Lock()
	adapter.snapshot, adapter.private = snapshot, private
	adapter.mu.Unlock()
	if snapshot.SessionID != adapter.sessionID && m.session.remote.secure != nil {
		adapter.close()
	}
}

func (m Model) startMobileRemote() (tea.Model, tea.Cmd) {
	if m.session == nil || m.running || m.approval.active || m.pendingTrust || m.resume.pending || m.remoteStarting || m.session.remote != nil {
		m.transcript.addSystem("remote control requires an idle session and no existing remote service")
		return m, nil
	}
	snapshot, private := m.mobileSnapshot()
	if private || snapshot.SessionID == "" {
		m.transcript.addSystem("finish the private operation before pairing")
		return m, nil
	}
	adapter := newMobileTUI(snapshot)
	owner, cwd := m.session, m.cwd
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.remoteStartSeq++
	seq := m.remoteStartSeq
	ready := make(chan struct{})
	go func() {
		<-ctx.Done()
		select {
		case <-ready:
		default:
			adapter.close()
		}
	}()
	m.remoteStarting, m.remoteStartCancel = true, cancel
	m.transcript.addSystem("Checking Tailscale Serve and opening a private pairing viewer…")
	return m, func() tea.Msg {
		server, pairURL, err := remotecontrol.StartSecure(ctx, adapter)
		if err != nil {
			adapter.close()
			return mobileStartMsg{owner: owner, ready: ready, seq: seq, err: err}
		}
		closeQR, err := agenttool.OpenRemotePairQR(ctx, cwd, pairURL)
		if err != nil {
			_ = server.Stop(context.Background())
			adapter.close()
			return mobileStartMsg{owner: owner, ready: ready, seq: seq, err: err}
		}
		if ctx.Err() != nil {
			closeQR()
			_ = server.Stop(context.Background())
			adapter.close()
			return mobileStartMsg{owner: owner, ready: ready, seq: seq, err: ctx.Err()}
		}
		return mobileStartMsg{owner: owner, adapter: adapter, server: server, closeQR: closeQR, ready: ready, seq: seq}
	}
}

func (m Model) finishMobileRemote(msg mobileStartMsg) (tea.Model, tea.Cmd) {
	if msg.ready != nil {
		close(msg.ready)
	}
	if !m.remoteStarting || m.remoteStartSeq != msg.seq || m.quitting || m.session != msg.owner || m.session == nil || m.session.remote != nil {
		if msg.closeQR != nil {
			go msg.closeQR()
		}
		if msg.server != nil {
			go msg.server.Stop(context.Background())
		}
		if msg.adapter != nil {
			msg.adapter.close()
		}
		return m, nil
	}
	m.remoteStarting = false
	if m.remoteStartCancel != nil {
		m.remoteStartCancel()
		m.remoteStartCancel = nil
	}
	if msg.err != nil {
		m.transcript.addSystem("remote control: " + msg.err.Error())
		return m, nil
	}
	select {
	case <-msg.adapter.done:
		if msg.closeQR != nil {
			go msg.closeQR()
		}
		if msg.server != nil {
			go msg.server.Stop(context.Background())
		}
		m.transcript.addSystem("Remote control startup was cancelled or timed out.")
		return m, nil
	default:
	}
	m.session.remote = &remoteSession{secure: msg.server, mobile: msg.adapter, closeQR: msg.closeQR, pairing: true}
	m.transcript.addSystem("Scan the QR in the private operator-only pane. Do not share it. Waiting for the phone…")
	return m, tea.Batch(m.waitMobileAction(), m.waitMobilePair(), m.waitMobileStopped())
}

func (m Model) waitMobileAction() tea.Cmd {
	if m.session == nil || m.session.remote == nil || m.session.remote.mobile == nil {
		return nil
	}
	a := m.session.remote.mobile
	return func() tea.Msg {
		select {
		case action := <-a.actions:
			return mobileActionMsg{adapter: a, action: action}
		case <-a.done:
			return nil
		}
	}
}

func (m Model) waitMobilePair() tea.Cmd {
	if m.session == nil || m.session.remote == nil || m.session.remote.secure == nil {
		return nil
	}
	s := m.session.remote.secure
	return func() tea.Msg {
		select {
		case req := <-s.PairRequests():
			return mobilePairMsg{server: s, request: req}
		case <-s.Done():
			return nil
		}
	}
}

func (m Model) waitMobileStopped() tea.Cmd {
	if m.session == nil || m.session.remote == nil || m.session.remote.secure == nil {
		return nil
	}
	s := m.session.remote.secure
	return func() tea.Msg { <-s.Done(); return mobileStoppedMsg{server: s} }
}

func (m Model) handleMobileAction(msg mobileActionMsg) (tea.Model, tea.Cmd) {
	if m.session == nil || m.session.remote == nil || m.session.remote.mobile != msg.adapter || m.session.header.ID != msg.adapter.sessionID {
		if msg.action.reply != nil {
			msg.action.reply <- errors.New("session changed")
		}
		return m, nil
	}
	reply := func(err error) {
		if msg.action.reply != nil {
			msg.action.reply <- err
		}
	}
	if msg.action.ctx != nil && msg.action.ctx.Err() != nil {
		reply(msg.action.ctx.Err())
		return m, m.waitMobileAction()
	}
	switch msg.action.kind {
	case "input":
		text := strings.TrimSpace(msg.action.text)
		if m.running || m.session.remote.pairing || m.startRunFn == nil || text == "" || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!") || ui.HasImageReferences(text) || m.mcpWizard != nil || m.mcpOAuth != nil || m.larkWizard != nil || m.mcpRemoval != nil || m.extensionSetupText != "" {
			reply(errors.New("input unavailable; use the local TUI"))
			return m, m.waitMobileAction()
		}
		m.transcript.addUser(text)
		next, cmd := m.startPrompt(text)
		reply(nil)
		m = next.(Model)
		return m, tea.Batch(cmd, m.waitMobileAction())
	case "answer":
		if !m.approval.active || m.approval.remoteID != msg.action.approvalID {
			reply(errors.New("approval is no longer pending"))
			return m, m.waitMobileAction()
		}
		if m.approval.remoteReq.Mode != "" && m.session.perms.Mode().String() != m.approval.remoteReq.Mode {
			reply(errors.New("permission mode changed"))
			return m, m.waitMobileAction()
		}
		if m.approval.remoteReq.Kind == judge.ApprovalTrust && m.approval.remoteReq.TrustDir != m.session.hookDeps.ProjectDir {
			reply(errors.New("approval target changed"))
			return m, m.waitMobileAction()
		}
		answer, err := remotecontrol.ApprovalAnswer(m.approval.remoteReq, msg.action.choice)
		if err != nil {
			reply(err)
			return m, m.waitMobileAction()
		}
		dialog := m.approval
		m.approval = approvalDialog{}
		m.deliverApproval(dialog, answer)
		m.relayout()
		reply(nil)
	case "disconnect":
		if m.approval.active && m.approval.remoteAnswerable() {
			dialog := m.approval
			m.approval = approvalDialog{}
			m.deliverApproval(dialog, judge.ApprovalAnswer{})
			m.relayout()
		}
	}
	return m, m.waitMobileAction()
}
