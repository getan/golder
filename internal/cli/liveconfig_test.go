package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/compaction"
)

// TestFormatContextWindow pins the compact rendering used by /model lists and
// switch confirmations: round catalog values stay readable ("200K", "1M"),
// odd ones keep up to one decimal of precision, and an unknown budget is the
// empty string so callers omit the field instead of printing a fake zero.
func TestFormatContextWindow(t *testing.T) {
	cases := map[int]string{
		0:       "",
		-1:      "",
		512:     "512",
		8_192:   "8.2K",
		16_384:  "16.4K",
		128_000: "128K",
		200_000: "200K",
		204_800: "204.8K",
		// 999950 rounds to 1000.0K and must promote to M instead of "1000K".
		999_950:   "1M",
		1_000_000: "1M",
		1_048_576: "1.05M",
		1_050_000: "1.05M",
		2_000_000: "2M",
	}
	for in, want := range cases {
		if got := FormatContextWindow(in); got != want {
			t.Errorf("FormatContextWindow(%d) = %q, want %q", in, got, want)
		}
	}
}

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

// TestLiveConfigCompactionSettings pins the [compaction] max_context wiring:
// the knob lowers the trigger point for the session's current window, the "N%"
// form re-resolves after a /model switch that changes the window, and an
// unset value leaves the defaults untouched.
func TestLiveConfigCompactionSettings(t *testing.T) {
	defaults := compaction.DefaultCompactionSettings
	if got := (&LiveConfig{ContextWindow: 1_000_000}).CompactionSettings(); got != defaults {
		t.Errorf("unset max_context changed settings: %+v", got)
	}

	abs, err := config.ParseMaxContext("300K")
	if err != nil {
		t.Fatalf("ParseMaxContext: %v", err)
	}
	live := &LiveConfig{ContextWindow: 1_000_000, MaxContext: abs}
	if got, want := live.CompactionSettings().UsableTokens(1_000_000), 270_000; got != want {
		t.Errorf("absolute max_context usable = %d, want %d", got, want)
	}

	// A percentage follows the window across a /model switch.
	pct, err := config.ParseMaxContext("50%")
	if err != nil {
		t.Fatalf("ParseMaxContext: %v", err)
	}
	live = &LiveConfig{ContextWindow: 200_000, MaxContext: pct}
	// max_context 50% => effective window 100k, and the reserve is the larger
	// of 16k and 10% of that window, so usable = 100k - 16384.
	if got, want := live.CompactionSettings().UsableTokens(200_000), 100_000-16_384; got != want {
		t.Errorf("50%% of 200k usable = %d, want %d", got, want)
	}
	live.ContextWindow = 1_000_000
	if got, want := live.CompactionSettings().UsableTokens(1_000_000), 450_000; got != want {
		t.Errorf("50%% of 1M usable = %d, want %d", got, want)
	}
}
