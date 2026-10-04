package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestModelCatalogCacheKeyNormalizes verifies the cache key is provider +
// normalized endpoint, so trailing slashes and stray whitespace never split
// one endpoint into two entries.
func TestModelCatalogCacheKeyNormalizes(t *testing.T) {
	want := "opencode-go|https://opencode.ai/zen/go/v1"
	for _, base := range []string{
		"https://opencode.ai/zen/go/v1",
		"https://opencode.ai/zen/go/v1/",
		"  https://opencode.ai/zen/go/v1  ",
	} {
		if got := ModelCatalogCacheKey("opencode-go", base); got != want {
			t.Errorf("ModelCatalogCacheKey(%q) = %q, want %q", base, got, want)
		}
	}
	if got := ModelCatalogCacheKey("ollama", ""); got != "ollama" {
		t.Errorf("empty base URL key = %q, want %q", got, "ollama")
	}
}

// TestModelCatalogCacheRoundTrip verifies store/read/freshness: a stored list
// comes back fresh, entries are independent per key, and a stale entry still
// returns its ids with fresh=false so callers can serve it as a fallback.
func TestModelCatalogCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)

	// Nothing cached yet, and an empty key is refused.
	if ids, fresh := CachedModelCatalog("openai|https://api.openai.com/v1"); ids != nil || fresh {
		t.Errorf("empty cache = (%v, %v), want (nil, false)", ids, fresh)
	}
	if ids, fresh := CachedModelCatalog("  "); ids != nil || fresh {
		t.Errorf("blank key = (%v, %v), want (nil, false)", ids, fresh)
	}

	keyA := ModelCatalogCacheKey("openai", "https://api.openai.com/v1")
	keyB := ModelCatalogCacheKey("zai", "https://api.z.ai/api/coding/paas/v4")
	StoreModelCatalog(keyA, []string{"m-a", "m-b"})
	StoreModelCatalog(keyB, []string{"glm-5.3"})
	// Storing key B must not clobber key A.
	if ids, fresh := CachedModelCatalog(keyA); !fresh || !reflect.DeepEqual(ids, []string{"m-a", "m-b"}) {
		t.Errorf("key A = (%v, %v), want ([m-a m-b], true)", ids, fresh)
	}
	if ids, fresh := CachedModelCatalog(keyB); !fresh || !reflect.DeepEqual(ids, []string{"glm-5.3"}) {
		t.Errorf("key B = (%v, %v), want ([glm-5.3], true)", ids, fresh)
	}

	// An empty list is not stored (a failed fetch must not wipe a good entry).
	StoreModelCatalog(keyA, nil)
	if ids, fresh := CachedModelCatalog(keyA); !fresh || len(ids) != 2 {
		t.Errorf("after empty store = (%v, %v), want the previous list", ids, fresh)
	}

	// Age the entry past the shared TTL: ids survive, freshness does not.
	var f modelCatalogFile
	data, err := os.ReadFile(filepath.Join(dir, modelCatalogFileName))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("cache JSON: %v", err)
	}
	e := f.Entries[keyA]
	e.CheckedAt = time.Now().Add(-25 * time.Hour)
	f.Entries[keyA] = e
	data, _ = json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, modelCatalogFileName), data, 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}
	if ids, fresh := CachedModelCatalog(keyA); fresh || !reflect.DeepEqual(ids, []string{"m-a", "m-b"}) {
		t.Errorf("stale entry = (%v, %v), want ([m-a m-b], false)", ids, fresh)
	}
}
