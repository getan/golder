// Package browserbinding owns a conversation's browser selection. It contains
// identity and presentation state, never operator credentials or permission to
// control a browser. The browser host owns the foreground tab within that view.
package browserbinding

import (
	"path/filepath"
	"strings"
	"sync"
)

type Endpoint struct {
	Socket    string
	BrowserID string
}

// Snapshot is immutable. Changed closes when a newer snapshot is available;
// readers always fetch the latest state rather than replaying queued events.
// Reveal advances only for an explicit foreground operation, not discovery or
// screenshots. Closing a GUI drawer therefore remains a local presentation act.
type Snapshot struct {
	SessionID string
	Endpoint  Endpoint
	Revision  uint64
	Reveal    uint64
	Changed   <-chan struct{}
}

type Binding struct {
	mu       sync.Mutex
	session  string
	endpoint Endpoint
	revision uint64
	reveal   uint64
	changed  chan struct{}
}

func New(session string) *Binding {
	return &Binding{session: session, changed: make(chan struct{})}
}

func (b *Binding) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{b.session, b.endpoint, b.revision, b.reveal, b.changed}
}

// Select is used by both the trusted GUI and Agent's admitted open/attach path.
func (b *Binding) Select(endpoint Endpoint) {
	endpoint.Socket = SocketKey(endpoint.Socket)
	if endpoint.Socket == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.endpoint = endpoint
	b.reveal++
	b.advance()
}

// Restore adopts an unambiguous existing view only on first discovery. A
// cleared/closed selection must never silently fall through to another view.
func (b *Binding) Restore(endpoint Endpoint) {
	endpoint.Socket = SocketKey(endpoint.Socket)
	if endpoint.Socket == "" || endpoint.BrowserID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.revision == 0 {
		b.endpoint = endpoint
		b.reveal++
		b.advance()
	}
}

// Present and Clear are compare-and-swap operations: a late completion from a
// previous browser cannot reveal or clear a newer binding.
func (b *Binding) Present(expected Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == expected.SessionID && b.revision == expected.Revision && b.endpoint == expected.Endpoint && b.endpoint.Socket != "" {
		b.reveal++
		b.advance()
	}
}

func (b *Binding) Clear(expected Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == expected.SessionID && b.revision == expected.Revision && b.endpoint == expected.Endpoint && b.endpoint.Socket != "" {
		b.endpoint = Endpoint{}
		b.advance()
	}
}

func (b *Binding) advance() {
	b.revision++
	close(b.changed)
	b.changed = make(chan struct{})
}

func SocketKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return filepath.Clean(path)
}
