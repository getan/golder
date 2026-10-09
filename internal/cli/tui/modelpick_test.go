package tui

// Tests for the model picker rows' Detail line: the catalog's context window
// and reasoning levels ride the row so the numbers behind the status bar and
// the /think flow are visible while choosing.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestModelPickItemsAnnotateCatalog pins the row detail: the window leads
// ("200K context"), levels follow, an unknown model gets neither, and a model
// the catalog knows only partially still shows what it has.
func TestModelPickItemsAnnotateCatalog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	fixture, err := json.Marshal(map[string]any{
		"version":    2,
		"checked_at": time.Now(),
		"providers": map[string]any{
			"openai": map[string]any{
				"m-full": map[string]any{
					"reasoning": true, "levels": []string{"low", "high"}, "context": 200000,
				},
				"m-window": map[string]any{"context": 1_000_000},
				"m-bare":   map[string]any{"reasoning": false},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reasoning-catalog.json"), fixture, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	items := modelPickItems("openai", []string{"m-full", "m-window", "m-bare", "m-unknown"})
	got := make(map[string]string, len(items))
	for _, it := range items {
		got[it.Title] = it.Detail
	}
	want := map[string]string{
		"m-full":    "200K context · reasoning: low|high",
		"m-window":  "1M context",
		"m-bare":    "",
		"m-unknown": "",
	}
	for id, wantDetail := range want {
		if got[id] != wantDetail {
			t.Errorf("modelPickItems[%s].Detail = %q, want %q", id, got[id], wantDetail)
		}
	}
}
