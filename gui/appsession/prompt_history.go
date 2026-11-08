package appsession

import "github.com/getan/golder/internal/prompthistory"

// SeedPromptHistory includes existing sessions when prompt history is first
// browsed, using the same bounded, one-time backfill as the TUI.
func (c *Controller) SeedPromptHistory(path string) {
	c.mu.Lock()
	store := c.store
	c.mu.Unlock()
	prompthistory.SeedFromSessions(store, path)
}
