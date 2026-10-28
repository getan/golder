package extensions

import (
	"context"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// KnownTools is used for validating explicit operator tool names before the
// allow/deny filter. It is never registered directly for execution.
func (m *Manager) KnownTools() []agentcore.AgentTool {
	if m.opts.NoTools {
		return nil
	}
	var out []agentcore.AgentTool
	for _, snap := range m.List() {
		for _, v := range snap.Tools {
			if v.State != "invalid" {
				out = append(out, &tool{manager: m, ref: snap.Ref, spec: v.Spec, name: v.ModelName})
			}
		}
	}
	return out
}

// RefreshDirty only uses already-established sessions. No new process or
// connection can be introduced by a list_changed notification.
func (m *Manager) RefreshDirty(ctx context.Context) {
	m.mu.RLock()
	var sources []*source
	for _, s := range m.sources {
		sources = append(sources, s)
	}
	m.mu.RUnlock()
	for _, s := range sources {
		if !s.enabled.Load() || !s.dirty.Load() || !s.mu.TryLock() {
			continue
		}
		if s.conn != nil {
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			cat, e := s.conn.Catalog(bounded)
			cancel()
			if e == nil {
				s.catalog = cat
				s.dirty.Store(false)
				m.revision.Add(1)
			} else {
				s.lastError = clean(e.Error(), 2048)
			}
			m.publish(s)
		}
		s.mu.Unlock()
	}
}
