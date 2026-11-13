package appsession

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/trust"
)

// ErrNotPending is returned when an approval id is unknown: it was answered
// already, withdrawn when the run ended, or never issued.
var ErrNotPending = errors.New("approval is no longer pending")

type pendingApproval struct {
	view ApprovalView
	req  judge.ApprovalRequest
	ctx  context.Context
	// reply is buffered so an answer that races a cancelled run is never lost.
	reply chan judge.ApprovalAnswer
}

// approvalKey keys the "allow for this session" memory: the tool plus its
// summary, so approving a build does not also approve an unrelated command.
func approvalKey(req judge.ApprovalRequest) string {
	// A GUI terminal can be an unrestricted user shell. Each terminal call
	// needs its own reviewed decision; a previous answer is not this call's.
	if req.Tool == "terminal" {
		return ""
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		return ""
	}
	// A command approval is not an approval for a different cache, a writable
	// module store, or an escape. Include the exact capability question.
	needs := make([]string, 0, len(req.Needs))
	for _, n := range req.Needs {
		needs = append(needs, fmt.Sprintf("%v|%s", n, n.Key))
	}
	sort.Strings(needs)
	return req.Tool + "\x00" + summary + "\x00" + strings.Join(needs, "\x00")
}

// confirmApproval is the permission gate's dialog. It publishes the request and
// blocks until the view answers or the run is cancelled; an unanswered request
// is reported as such, which the gate treats as fail-closed.
func (c *Controller) confirmApproval(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
	if key := approvalKey(req); key != "" {
		c.mu.Lock()
		approved, denied := c.approvedForSession[key], c.deniedForSession[key]
		c.mu.Unlock()
		switch {
		case approved:
			return judge.ApprovalAnswer{Approve: true, Always: true, Answered: true}
		case denied:
			return judge.ApprovalAnswer{Answered: true}
		}
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return judge.ApprovalAnswer{}
	}
	c.nextApproval++
	id := fmt.Sprintf("approval-%d", c.nextApproval)
	view := approvalViewOf(id, req)
	view.RunID = c.runID
	view.ToolCallID = agentcore.ToolCallIDFromContext(ctx)
	if view.Origin == "" {
		view.Origin = agentcore.OriginFromContext(ctx)
	}
	p := &pendingApproval{view: view, req: req, ctx: ctx, reply: make(chan judge.ApprovalAnswer, 1)}
	c.pending = append(c.pending, p)
	c.mu.Unlock()

	c.emit(EventApproval, func(ev *Event) {
		v := view
		ev.Approval = &v
		ev.ID = id
	})

	var ans judge.ApprovalAnswer
	select {
	case ans = <-p.reply:
	case <-ctx.Done():
		ans = judge.ApprovalAnswer{}
	case <-c.done:
		ans = judge.ApprovalAnswer{}
	}

	c.mu.Lock()
	for i, q := range c.pending {
		if q == p {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			break
		}
	}
	c.answered[id] = true
	c.mu.Unlock()
	c.emit(EventApprovalDone, func(ev *Event) {
		ev.ID = id
		if ans.Answered && ans.Approve {
			ev.Status = &Status{}
		}
	})

	if key := approvalKey(req); key != "" && ans.Answered {
		c.mu.Lock()
		switch {
		case ans.Approve && ans.Always:
			c.approvedForSession[key] = true
			delete(c.deniedForSession, key)
		case !ans.Approve:
			c.deniedForSession[key] = true
		}
		c.mu.Unlock()
	}
	return ans
}

// RespondApproval answers one pending approval.
func (c *Controller) RespondApproval(id string, a Answer) error {
	c.mu.Lock()
	if c.answered[id] {
		c.mu.Unlock()
		return ErrNotPending
	}
	var p *pendingApproval
	for i, q := range c.pending {
		if q.view.ID == id {
			p = q
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if p == nil {
		return ErrNotPending
	}

	ans := judge.ApprovalAnswer{Answered: true}
	switch a {
	case AnswerOnce:
		ans.Approve = true
	case AnswerSession:
		ans.Approve, ans.Always = true, true
	}
	// The trust question's answer is a session grant for the folder; persist it
	// when the person asked for that, so a later session does not ask again.
	if p.req.Kind == judge.ApprovalTrust && ans.Approve {
		c.mu.Lock()
		mgr, cwd := c.trust, c.cwd
		c.mu.Unlock()
		if mgr != nil {
			if a == AnswerSession {
				_ = mgr.SetDecision(cwd, trust.Trusted)
				c.wireHooks(context.Background(), false)
			} else {
				mgr.SetSessionTrust(cwd)
			}
		}
	}

	select {
	case p.reply <- ans:
	default:
		return ErrNotPending
	}
	return nil
}

// PendingApprovals returns the approvals waiting for an answer.
func (c *Controller) PendingApprovals() []ApprovalView {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ApprovalView, 0, len(c.pending))
	for _, p := range c.pending {
		out = append(out, p.view)
	}
	return out
}

func approvalViewOf(id string, req judge.ApprovalRequest) ApprovalView {
	kind := "consent"
	if req.Kind == judge.ApprovalTrust {
		kind = "trust"
	}
	needs := make([]string, 0, len(req.Needs))
	for _, n := range req.Needs {
		needs = append(needs, fmt.Sprint(n))
	}
	return ApprovalView{
		ID:        id,
		Kind:      kind,
		Tool:      req.Tool,
		Summary:   req.Summary,
		Rationale: req.Rationale,
		Source:    req.Source,
		Level:     req.Level.String(),
		Mode:      req.Mode,
		Needs:     needs,
		TrustDir:  req.TrustDir,
		Origin:    req.Origin,
	}
}
