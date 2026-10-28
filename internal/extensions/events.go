package extensions

import (
	"encoding/json"
	"time"

	"github.com/getan/golder/internal/agentcore"
	legacy "github.com/getan/golder/internal/plugin"
)

// Observe only queues host-owned metadata. Message bodies, tool arguments and
// result text are never copied into an extension event subscription.
func (m *Manager) Observe(ev agentcore.AgentEvent) {
	if m.closed.Load() {
		return
	}
	data := map[string]any{}
	switch e := ev.(type) {
	case agentcore.AgentEndEvent:
		data["messageCount"] = len(e.Messages)
	case agentcore.ToolExecutionStartEvent:
		data["toolName"] = e.ToolName
		data["toolCallId"] = e.ToolCallID
	case agentcore.ToolExecutionEndEvent:
		data["toolName"] = e.ToolName
		data["toolCallId"] = e.ToolCallID
		data["isError"] = e.IsError
	case agentcore.TurnEndEvent:
		data["stopReason"] = e.Message.StopReason
	}
	raw, _ := json.Marshal(data)
	select {
	case m.events <- legacy.EventParams{Type: ev.EventType(), Data: raw}:
	default:
	}
}

func (m *Manager) deliverEvents() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case ev := <-m.events:
			m.mu.RLock()
			var sources []*source
			for _, s := range m.sources {
				sources = append(sources, s)
			}
			m.mu.RUnlock()
			for _, s := range sources {
				if !s.enabled.Load() || !s.mu.TryLock() {
					continue
				}
				rpc, ok := s.conn.(*rpcBackend)
				subscribed := false
				for _, kind := range s.catalog.Events {
					if kind == ev.Type {
						subscribed = true
						break
					}
				}
				if ok && subscribed {
					done := make(chan error, 1)
					go func() { done <- rpc.client.Notify("event", ev) }()
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-done:
					case <-timer.C:
						if s.cancel != nil {
							s.cancel()
						}
						s.lastError = "event delivery timed out; plugin stopped"
						s.state = "failed"
						m.publish(s)
					case <-m.ctx.Done():
					}
					timer.Stop()
				}
				s.mu.Unlock()
			}
		}
	}
}
