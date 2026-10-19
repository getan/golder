package runtime

// SetMemoryRecall turns the per-turn memory reminder on or off on every provider
// already registered, so `/memory recall on|off` takes effect in the running
// session instead of the next one. It reports whether a memory provider was
// found (a session with memory disabled has none).
func (r *ReminderRegistry) SetMemoryRecall(on bool) bool {
	if r == nil {
		return false
	}
	found := false
	for _, p := range r.providers {
		if m, ok := p.(*MemoryReminderProvider); ok {
			m.Disabled = !on
			found = true
		}
	}
	return found
}
