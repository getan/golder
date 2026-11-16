package mobileagent

import (
	"context"
	"errors"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/runtime"
)

// pauseBarrier stops admitting new tool calls before the host gives the phone
// to the person. A call admitted before the pause must finish before the host
// reports that the handoff is ready. The generation invalidates calls planned
// before the person changed the screen; they must not be replayed on handback.
type pauseBarrier struct {
	mu         sync.Mutex
	paused     bool
	wake       chan struct{}
	active     int
	generation uint64
	turn       uint64
	notified   uint64
}

const handbackGuidance = "The person temporarily took over the phone and has handed it back. The screen may have changed. Re-observe before acting; do not replay an earlier action."

func (p *pauseBarrier) pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.paused {
		p.paused = true
		p.generation++
		p.wake = make(chan struct{})
	}
}

func (p *pauseBarrier) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		p.paused = false
		close(p.wake)
	}
}

func (p *pauseBarrier) status() (paused, quiescent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused, p.active == 0
}

// waitLocked releases the mutex while waiting and returns with it held.
func (p *pauseBarrier) waitLocked(ctx context.Context) error {
	for p.paused {
		wake := p.wake
		p.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			p.mu.Lock()
			return ctx.Err()
		}
		p.mu.Lock()
	}
	return ctx.Err()
}

func (p *pauseBarrier) beforeTurn(ctx context.Context, agentCtx *agentcore.AgentContext) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitLocked(ctx) == nil {
		if p.notified != p.generation {
			agentCtx.Messages = append(agentCtx.Messages, agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(handbackGuidance)},
			})
			p.notified = p.generation
		}
		p.turn = p.generation
	}
}

func (p *pauseBarrier) begin(ctx context.Context, _ agentcore.AgentToolCall) (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.waitLocked(ctx); err != nil {
		return nil, err
	}
	if p.turn != p.generation {
		return nil, errors.New("the person took over the phone after this action was planned; the action was not executed. Re-observe the current screen and decide again")
	}
	p.active++
	return func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}, nil
}

func (p *pauseBarrier) onStop(ctx context.Context, _ *agentcore.AgentContext) *runtime.StopDecision {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitLocked(ctx) != nil || p.turn == p.generation {
		return nil
	}
	p.notified = p.generation
	return &runtime.StopDecision{Block: true, Guidance: handbackGuidance}
}
