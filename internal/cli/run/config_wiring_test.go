package run

// TestNewConfigEnablesCompaction pins the shared-wiring default that headless
// (-p / stream-json) and the Agent SDK depend on: NewConfig must set
// Compaction, or every run assembled through it has auto-compaction silently
// disabled (`Enabled` false) even when its context window is known. The
// window itself stays the caller's gate: leaving it 0 still disables
// compaction, which the second case asserts.

import (
	"testing"

	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/provider"
)

func TestNewConfigEnablesCompaction(t *testing.T) {
	creds := provider.NewCredentialStore(nil)
	cfg := NewConfig("m", "p", "", nil, creds, nil, nil, nil, nil)

	if !cfg.Compaction.Enabled {
		t.Fatal("NewConfig must enable auto-compaction by default")
	}
	if got, want := cfg.Compaction.HeadroomPercent, compaction.DefaultCompactionSettings.HeadroomPercent; got != want {
		t.Errorf("HeadroomPercent = %d, want the default %d", got, want)
	}
	if got, want := cfg.Compaction.ReserveTokens, compaction.DefaultCompactionSettings.ReserveTokens; got != want {
		t.Errorf("ReserveTokens = %d, want the default %d", got, want)
	}
	// Unknown window stays a no-op: ShouldCompact never fires without it.
	if compaction.ShouldCompact(999_999, cfg.ContextWindow, cfg.Compaction) {
		t.Error("compaction must stay inactive while the context window is unknown")
	}
}
