// Package continuationtest supplies a credential-free protocol fixture for
// frontend tests. Production callers use authenticated browserhost connections.
package continuationtest

import (
	"context"
	"errors"
	"sync"

	"github.com/getan/golder/internal/browsercontinuation"
	"github.com/getan/kitty-browse/browserhost"
)

type Host struct {
	mu      sync.Mutex
	state   browserhost.InteractionState
	calls   int
	Changed chan struct{}
}

func New() *Host {
	return &Host{state: browserhost.InteractionState{BrowserID: "browser-test", InstanceEpoch: "epoch-test", Paused: true, PauseGeneration: 1, Reason: "assistance_required", ContinuationVersion: 1}, Changed: make(chan struct{}, 1)}
}

func (h *Host) Register(c *browsercontinuation.Coordinator) {
	s, _ := h.State(context.Background())
	c.Register("view-test", s.BrowserID, s.InstanceEpoch, "https://example.test", h)
	c.Poll(context.Background())
}

func (h *Host) State(context.Context) (browserhost.InteractionState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, nil
}

func (h *Host) ContinueChecked(ctx context.Context, req browserhost.ContinueRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !h.state.Paused || req.BrowserID != h.state.BrowserID || req.InstanceEpoch != h.state.InstanceEpoch || req.PauseGeneration != h.state.PauseGeneration {
		return errors.New("browser_pause_changed")
	}
	h.state.Paused, h.state.ResumedGeneration = false, h.state.PauseGeneration
	select {
	case h.Changed <- struct{}{}:
	default:
	}
	return nil
}

func (h *Host) ContinuationOutcome(context.Context, browserhost.ContinueRequest) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.state.Paused {
		return "resumed", nil
	}
	return "not_consumed", nil
}

func (h *Host) Calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}
