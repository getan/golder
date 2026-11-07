package appsession

import (
	"errors"
	"sync"
)

// BeginPrivateAuthorization prevents every controller owned by this GUI from
// starting an agent turn while the operator is interacting with a login page.
// It refuses to start while any existing turn is active. The returned release
// must be called after the browser has fully closed.
func (h *SessionHub) BeginPrivateAuthorization() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.authorizing || h.current == nil || len(h.entries) == 0 {
		return nil, errors.New("private authorization is unavailable or already active")
	}
	h.authorizing = true
	var blocked []*Controller
	for _, entry := range h.entries {
		c := entry.controller
		c.mu.Lock()
		if c.closed || c.running || c.privateAuthorization || c.extensionMutation {
			c.mu.Unlock()
			for _, previous := range blocked {
				previous.mu.Lock()
				previous.privateAuthorization = false
				previous.mu.Unlock()
			}
			h.authorizing = false
			return nil, errors.New("finish all GUI agent turns before private authorization")
		}
		c.privateAuthorization = true
		c.mu.Unlock()
		blocked = append(blocked, c)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, c := range blocked {
				c.mu.Lock()
				c.privateAuthorization = false
				c.mu.Unlock()
			}
			h.authorizing = false
		})
	}, nil
}
