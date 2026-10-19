package provider

import (
	"os"
	"sync"
	"testing"
	"time"
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
	if w, served, ok := CachedWire("p", "deepseek-v4.1-flash"); !ok || w != WireChat || served {
		t.Fatalf("model lookup = %v,served=%v,%v, want chat,false,true", w, served, ok)
	}
	if w, _, ok := CachedWire("p", "deepseek-v4-pro"); !ok || w != WireChat {
		t.Fatalf("family lookup = %v,%v, want chat,true", w, ok)
	}
	if _, err := os.ReadFile(wireCachePath()); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	resetWireCache()
	if w, _, ok := CachedWire("p", "deepseek-v4-pro"); !ok || w != WireChat {
		t.Fatalf("after reload = %v,%v, want chat,true", w, ok)
	}
}

// TestWireCacheRemembersWhereTheAnswerCameFrom: the two sources are not worth
// the same — an answer a real request produced may overturn the shared family
// table, a probe's may not (see cachedWireFor) — so the entry carries the
// distinction, and it survives a reload.
func TestWireCacheRemembersWhereTheAnswerCameFrom(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	resetWireCache()
	storeServedWire("p", "glm-5.3", WireChat)
	resetWireCache()
	if w, served, ok := CachedWire("p", "glm-5.3"); !ok || w != WireChat || !served {
		t.Fatalf("after reload = %v,served=%v,%v, want chat,true,true", w, served, ok)
	}
	// An entry written before the field existed reads as probe-grade: the
	// conservative side, where the first turn that needs an answer re-learns it.
	raw := `{"p\u0000glm-5.3":{"wire":"chat","at":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`
	if err := os.WriteFile(wireCachePath(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	resetWireCache()
	if _, served, ok := CachedWire("p", "glm-5.3"); !ok || served {
		t.Fatalf("old entry served=%v,%v, want false,true", served, ok)
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
	if _, _, ok := CachedWire("p", "deepseek-v4.1-flash"); ok {
		t.Fatal("expired entry still answered")
	}
}
