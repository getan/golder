package appsession

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/remotecontrol"
)

// MobileAdapter projects one controller without consuming its event channel.
// Full snapshots are taken under the controller lock, so the GUI's existing
// SessionHub remains the sole consumer of Controller.Events.
type MobileAdapter struct {
	controller *Controller
	sessionID  string
	mu         sync.Mutex
	ids        map[*pendingApproval]string
	byID       map[string]*pendingApproval
}

func NewMobileAdapter(c *Controller) (*MobileAdapter, error) {
	if c == nil {
		return nil, errors.New("no GUI session")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.running || c.header.ID == "" || c.readOnly || c.privateAuthorization || c.extensionMutation {
		return nil, errors.New("GUI session is unavailable for remote control")
	}
	return &MobileAdapter{controller: c, sessionID: c.header.ID, ids: make(map[*pendingApproval]string), byID: make(map[string]*pendingApproval)}, nil
}

func (a *MobileAdapter) Done() <-chan struct{} { return a.controller.Done() }

func (a *MobileAdapter) Snapshot() (remotecontrol.MobileSnapshot, error) {
	c := a.controller
	c.mu.Lock()
	if c.closed || c.header.ID != a.sessionID || c.privateAuthorization || c.extensionMutation {
		c.mu.Unlock()
		return remotecontrol.MobileSnapshot{}, errors.New("GUI session is unavailable")
	}
	out := remotecontrol.MobileSnapshot{SessionID: a.sessionID, Frontend: "GUI", Project: c.cwd, Title: c.title, Permission: c.perms.Mode().String(), Running: c.running}
	start := max(len(c.items)-200, 0)
	out.Truncated = start > 0
	for _, item := range c.items[start:] {
		message := remotecontrol.MobileMessage{ID: item.ID}
		switch item.Kind {
		case ItemUser:
			message.Role, message.Text = "user", item.Text
		case ItemAssistant:
			message.Role, message.Text = "assistant", item.Text
		case ItemTool:
			if item.Tool == nil {
				continue
			}
			message.Role, message.Text = "tool", item.Tool.Name
			if item.Tool.Output != "" {
				message.Text += "\n" + item.Tool.Output
			}
		default:
			continue
		}
		out.Messages = append(out.Messages, message)
	}
	pending := append([]*pendingApproval(nil), c.pending...)
	c.mu.Unlock()

	a.mu.Lock()
	defer a.mu.Unlock()
	live := make(map[*pendingApproval]bool, len(pending))
	for _, p := range pending {
		if p.ctx != nil && p.ctx.Err() != nil {
			continue
		}
		live[p] = true
		id := a.ids[p]
		if id == "" {
			var err error
			id, err = remotecontrol.NewApprovalID()
			if err != nil {
				return remotecontrol.MobileSnapshot{}, err
			}
			a.ids[p], a.byID[id] = id, p
		}
		req := p.req
		if req.Origin == "" {
			req.Origin = p.view.Origin
		}
		view := remotecontrol.ProjectApproval(id, req)
		view.RunID, view.ToolCallID = p.view.RunID, p.view.ToolCallID
		out.Approvals = append(out.Approvals, view)
	}
	for p, id := range a.ids {
		if !live[p] {
			delete(a.ids, p)
			delete(a.byID, id)
		}
	}
	return out, nil
}

func (a *MobileAdapter) Submit(ctx context.Context, text string) error {
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return errors.New("slash commands require the local GUI")
	}
	return a.controller.SubmitRemote(ctx, a.sessionID, text)
}

func (a *MobileAdapter) Answer(ctx context.Context, id, choice string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	p := a.byID[id]
	a.mu.Unlock()
	if p == nil {
		return ErrNotPending
	}
	answer, err := remotecontrol.ApprovalAnswer(p.req, choice)
	if err != nil {
		return err
	}
	return a.resolve(p, answer)
}

// resolve shares the controller's pending list with the local GUI. Removing
// under the same lock gives exactly one winner and preserves the original gate.
func (a *MobileAdapter) resolve(p *pendingApproval, answer judge.ApprovalAnswer) error {
	c := a.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || (answer.Answered && (c.privateAuthorization || c.extensionMutation)) || c.header.ID != a.sessionID || (p.ctx != nil && p.ctx.Err() != nil) {
		return ErrNotPending
	}
	if answer.Answered && p.req.Mode != "" && c.perms.Mode().String() != p.req.Mode {
		return ErrNotPending
	}
	if answer.Answered && p.req.Kind == judge.ApprovalTrust && p.req.TrustDir != c.cwd {
		return ErrNotPending
	}
	for i, current := range c.pending {
		if current == p {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			select {
			case p.reply <- answer:
				return nil
			default:
				return ErrNotPending
			}
		}
	}
	return ErrNotPending
}

func (a *MobileAdapter) Disconnected() {
	a.mu.Lock()
	pending := make([]*pendingApproval, 0, len(a.ids))
	for p := range a.ids {
		if len(remotecontrol.ProjectApproval("pending", p.req).Choices) != 0 {
			pending = append(pending, p)
		}
	}
	a.ids = make(map[*pendingApproval]string)
	a.byID = make(map[string]*pendingApproval)
	a.mu.Unlock()
	for _, p := range pending {
		_ = a.resolve(p, judge.ApprovalAnswer{})
	}
}
