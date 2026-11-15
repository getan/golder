package mobilebridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMobileModelCacheSnapshot(t *testing.T) {
	dir := t.TempDir()
	key := "openai|https://api.openai.com/v1"
	path := filepath.Join(dir, "model-catalog.json")
	if err := writeMobileModelCache(path, key, mobileModelCacheEntry{
		CheckedAt: time.Now().Add(-8 * 24 * time.Hour),
		IDs:       []string{"gpt-test"},
	}); err != nil {
		t.Fatal(err)
	}
	var entry mobileModelCacheEntry
	if !readMobileModelCache(path, key, &entry) {
		t.Fatal("cache entry was not read")
	}
	if len(entry.IDs) != 1 || entry.IDs[0] != "gpt-test" {
		t.Fatalf("unexpected cache entry: %+v", entry)
	}
	raw, err := json.Marshal(mobileModelResult{IDs: entry.IDs, Cached: true, Fresh: false})
	if err != nil {
		t.Fatal(err)
	}
	var result mobileModelResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Cached || result.Fresh || len(result.IDs) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	_ = os.Remove(path)
}
