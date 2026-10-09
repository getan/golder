package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResolveContextWindow pins the cli-level resolution: the models.dev value
// wins when the cached catalog knows the model, and the historical 1M default
// is kept for anything the catalog does not cover.
func TestResolveContextWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	// Mirrors provider.reasoningCatalogFileName; the file is the same 24h cache
	// the reasoning ladder reads. Version 2 is the schema that carries context.
	cache := `{"version":2,"checked_at":"` + time.Now().UTC().Format(time.RFC3339) +
		`","providers":{"anthropic":{"claude-sonnet-4-5":{"reasoning":true,"context":200000}},` +
		`"minimax":{"MiniMax-M2.5":{"reasoning":true,"context":1000000}}}}`
	if err := os.WriteFile(filepath.Join(dir, "reasoning-catalog.json"), []byte(cache), 0o644); err != nil {
		t.Fatalf("write catalog cache: %v", err)
	}

	if got := ResolveContextWindow("anthropic", "claude-sonnet-4-5"); got != 200000 {
		t.Errorf("catalog window = %d, want 200000", got)
	}
	// A gateway id with different casing than models.dev still resolves.
	if got := ResolveContextWindow("minimax", "minimax-m2.5"); got != 1000000 {
		t.Errorf("case-folded catalog window = %d, want 1000000", got)
	}
	if got, want := ResolveContextWindow("anthropic", "not-in-catalog"), DefaultContextWindow; got != want {
		t.Errorf("unknown model window = %d, want fallback %d", got, want)
	}
	if got, want := ResolveContextWindow("custom-gateway", "m"), DefaultContextWindow; got != want {
		t.Errorf("unknown provider window = %d, want fallback %d", got, want)
	}

	live := &LiveConfig{ProviderName: "anthropic", Model: "claude-sonnet-4-5", ContextWindow: DefaultContextWindow}
	live.RefreshContextWindow()
	if live.ContextWindow != 200000 {
		t.Errorf("RefreshContextWindow = %d, want 200000", live.ContextWindow)
	}
	live.Model = "not-in-catalog"
	live.RefreshContextWindow()
	if live.ContextWindow != DefaultContextWindow {
		t.Errorf("RefreshContextWindow fallback = %d, want %d", live.ContextWindow, DefaultContextWindow)
	}
}
