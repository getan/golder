// Package terminalaccess keeps GUI terminal permissions separate from both
// session labels and terminal contents. Only the operator-facing host grants
// access; model-supplied names are never credentials.
package terminalaccess

import (
	"errors"
	"sync"
	"sync/atomic"
)

var ErrDenied = errors.New("terminal access denied")

type pane struct {
	mu          sync.Mutex
	input       sync.Mutex
	userPending atomic.Int32
	project     string
	readers     map[string]bool
	writer      string
	creator     string
	ended       bool
	closed      bool
	changed     chan struct{}
}

func (p *pane) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// Registry is owned by one GUI process. Agent and pane IDs must be unique for
// their lifetime and never reused after removal.
type Registry struct {
	mu         sync.RWMutex
	panes      map[string]*pane
	agents     map[string]string
	seenPanes  map[string]bool
	seenAgents map[string]bool
}

func New() *Registry {
	return &Registry{panes: make(map[string]*pane), agents: make(map[string]string),
		seenPanes: make(map[string]bool), seenAgents: make(map[string]bool)}
}

func (r *Registry) AddAgent(id, project string) error {
	if id == "" || project == "" {
		return ErrDenied
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seenAgents[id] {
		return ErrDenied
	}
	r.seenAgents[id] = true
	r.agents[id] = project
	return nil
}

func (r *Registry) AddPane(id, project string) error {
	if id == "" || project == "" {
		return ErrDenied
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seenPanes[id] {
		return ErrDenied
	}
	r.seenPanes[id] = true
	r.panes[id] = &pane{project: project, readers: make(map[string]bool), changed: make(chan struct{})}
	return nil
}

// AddAgentPane atomically validates the creator and, for a split, its current
// write lease on the exact parent. A created pane starts with that Agent's read
// and write access; no later focus or project switch can change its owner.
func (r *Registry) AddAgentPane(id, agentID, parentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	project, alive := r.agents[agentID]
	if !alive || id == "" || r.seenPanes[id] {
		return ErrDenied
	}
	if parentID != "" {
		parent := r.panes[parentID]
		if parent == nil {
			return ErrDenied
		}
		parent.mu.Lock()
		allowed := !parent.closed && parent.project == project && parent.writer == agentID && parent.userPending.Load() == 0
		parent.mu.Unlock()
		if !allowed {
			return ErrDenied
		}
	}
	r.seenPanes[id] = true
	r.panes[id] = &pane{project: project, readers: map[string]bool{agentID: true}, writer: agentID, creator: agentID, changed: make(chan struct{})}
	return nil
}

// Grant is an operator action. Write access is exclusive: transfer must be
// explicitly approved when a different agent already holds it. Read and write
// are independent so granting input does not silently expose past output.
func (r *Registry) Grant(paneID, agentID string, read, write, transfer bool) error {
	r.mu.RLock()
	p, ok := r.panes[paneID]
	project, alive := r.agents[agentID]
	if !ok || !alive {
		r.mu.RUnlock()
		return ErrDenied
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	defer r.mu.RUnlock()
	if p.closed || p.project != project || write && (p.ended || p.userPending.Load() != 0 || p.writer != "" && p.writer != agentID && !transfer) {
		return ErrDenied
	}
	if read {
		p.readers[agentID] = true
	}
	if write {
		p.writer = agentID
	}
	if read || write {
		p.notifyLocked()
	}
	return nil
}

// Use serializes input and rechecks authority immediately before fn. Revocation
// stops future input; a write already dispatched may finish. The state mutex is
// not held during a potentially blocking PTY write, so the UI can revoke it.
func (r *Registry) Use(paneID, agentID string, write bool, fn func() error) error {
	r.mu.RLock()
	p, ok := r.panes[paneID]
	if !ok || fn == nil {
		r.mu.RUnlock()
		return ErrDenied
	}
	r.mu.RUnlock()
	if write {
		p.input.Lock()
		defer p.input.Unlock()
	}
	r.mu.RLock()
	current, exists := r.panes[paneID]
	project, alive := r.agents[agentID]
	if !exists || current != p || !alive {
		r.mu.RUnlock()
		return ErrDenied
	}
	p.mu.Lock()
	if p.closed || p.project != project || write && (p.ended || p.writer != agentID || p.userPending.Load() != 0) || !write && !p.readers[agentID] {
		p.mu.Unlock()
		r.mu.RUnlock()
		return ErrDenied
	}
	if write {
		p.mu.Unlock()
		r.mu.RUnlock()
		return fn()
	}
	defer p.mu.Unlock()
	defer r.mu.RUnlock()
	return fn()
}

// UserInput takes back exclusive write control before writing bytes from the
// local terminal view. The same pane lock guards agent writes and transfer, so
// a queued user write cannot interleave with an authorized agent operation.
func (r *Registry) UserInput(paneID string, fn func() error) (string, error) {
	r.mu.RLock()
	p := r.panes[paneID]
	if p == nil || fn == nil {
		r.mu.RUnlock()
		return "", ErrDenied
	}
	p.userPending.Add(1)
	defer p.userPending.Add(-1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		r.mu.RUnlock()
		return "", ErrDenied
	}
	previous := p.writer
	p.writer = ""
	if previous != "" {
		p.notifyLocked()
	}
	p.mu.Unlock()
	r.mu.RUnlock()
	p.input.Lock()
	defer p.input.Unlock()
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return previous, ErrDenied
	}
	return previous, fn()
}

// SystemInput serializes terminal-protocol replies while the view is locked to
// an Agent. The caller must ensure local user input is disabled first.
func (r *Registry) SystemInput(paneID string, fn func() error) error {
	r.mu.RLock()
	p := r.panes[paneID]
	r.mu.RUnlock()
	if p == nil || fn == nil {
		return ErrDenied
	}
	p.input.Lock()
	defer p.input.Unlock()
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrDenied
	}
	return fn()
}

// Access reports an agent's current rights without exposing the identities of
// other readers. Callers still have to use Use immediately before an action.
func (r *Registry) Access(paneID, agentID string) (read, write bool, owner string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.panes[paneID]
	project, alive := r.agents[agentID]
	if !ok || !alive {
		return false, false, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.project != project {
		return false, false, ""
	}
	return p.readers[agentID], p.writer == agentID, p.writer
}

// WatchRead returns an event closed when pane permissions or lifecycle change.
// It checks read access while subscribing, so callers cannot miss revocation.
func (r *Registry) WatchRead(paneID, agentID string) (<-chan struct{}, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.panes[paneID]
	project, alive := r.agents[agentID]
	if p == nil || !alive {
		return nil, ErrDenied
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.project != project || !p.readers[agentID] {
		return nil, ErrDenied
	}
	return p.changed, nil
}

// Owner is display-only; authorization must still be checked by Use.
func (r *Registry) Owner(paneID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.panes[paneID]
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writer
}

// TakeBack revokes whichever Agent currently has write control without
// changing separately granted read access.
func (r *Registry) TakeBack(paneID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p := r.panes[paneID]; p != nil {
		p.mu.Lock()
		if p.writer != "" {
			p.writer = ""
			p.notifyLocked()
		}
		p.mu.Unlock()
	}
}

// MarkExited revokes input while retaining explicit readers and the creator's
// ability to close its finished pane. It does not remove the displayed output.
func (r *Registry) MarkExited(paneID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p := r.panes[paneID]; p != nil {
		p.mu.Lock()
		p.ended, p.writer = true, ""
		p.notifyLocked()
		p.mu.Unlock()
	}
}

func (r *Registry) CanClose(paneID, agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.panes[paneID]
	project, alive := r.agents[agentID]
	if p == nil || !alive {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && p.project == project && (p.writer == agentID || p.ended && p.creator == agentID && p.readers[agentID])
}

// RemoveAgentPane atomically checks the exact live writer before removing the
// pane. Closing its underlying process happens after the registry lock releases.
func (r *Registry) RemoveAgentPane(paneID, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.panes[paneID]
	project, alive := r.agents[agentID]
	if p == nil || !alive {
		return ErrDenied
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.project != project || !(p.writer == agentID || p.ended && p.creator == agentID && p.readers[agentID]) {
		return ErrDenied
	}
	p.closed = true
	p.notifyLocked()
	delete(r.panes, paneID)
	return nil
}

func (r *Registry) Revoke(paneID, agentID string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p := r.panes[paneID]; p != nil {
		p.mu.Lock()
		changed := p.readers[agentID] || p.writer == agentID
		delete(p.readers, agentID)
		if p.writer == agentID {
			p.writer = ""
		}
		if changed {
			p.notifyLocked()
		}
		p.mu.Unlock()
	}
}

func (r *Registry) RemovePane(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.panes[id]; p != nil {
		p.mu.Lock()
		p.closed = true
		p.notifyLocked()
		delete(r.panes, id)
		p.mu.Unlock()
	}
}

func (r *Registry) RemoveAgent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
	for _, p := range r.panes {
		p.mu.Lock()
		changed := p.readers[id] || p.writer == id
		delete(p.readers, id)
		if p.writer == id {
			p.writer = ""
		}
		if changed {
			p.notifyLocked()
		}
		p.mu.Unlock()
	}
}
