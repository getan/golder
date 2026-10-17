package provider

import (
	"os"
	"sync"
	"testing"
)

// resetWireCache drops the in-process map so a test observes a fresh load from
// the file, the way a restart does.
func resetWireCache() {
	wireCacheOnce = sync.Once{}
	wireCacheMu.Lock()
	wireCache = nil
	wireCacheMu.Unlock()
}

// TestWireCacheRoundTrip: a confirmed wire answers for the model and, through
// the family key, for its siblings — and it survives a fresh load.
func TestWireCacheRoundTrip(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	storeWire("p", "deepseek-v4.1-flash", WireChat)
	storeWire("p", "family:deepseek", WireChat)
	if w, ok := CachedWire("p", "deepseek-v4.1-flash"); !ok || w != WireChat {
		t.Fatalf("model lookup = %v,%v, want chat,true", w, ok)
	}
	if w, ok := CachedWire("p", "deepseek-v4-pro"); !ok || w != WireChat {
		t.Fatalf("family lookup = %v,%v, want chat,true", w, ok)
	}
	if _, err := os.ReadFile(wireCachePath()); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	resetWireCache()
	if w, ok := CachedWire("p", "deepseek-v4-pro"); !ok || w != WireChat {
		t.Fatalf("after reload = %v,%v, want chat,true", w, ok)
	}
}

// TestWireCacheTTL: an expired answer is treated as absent, so a gateway that
// changed its routing is asked again.
func TestWireCacheTTL(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	raw := `{"p\u0000deepseek-v4.1-flash":{"wire":"chat","at":"2020-01-01T00:00:00Z"}}`
	if err := os.WriteFile(wireCachePath(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	resetWireCache()
	if _, ok := CachedWire("p", "deepseek-v4.1-flash"); ok {
		t.Fatal("expired entry still answered")
	}
}
