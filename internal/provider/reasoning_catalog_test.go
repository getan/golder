package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// withReasoningCatalog installs a fixed in-process catalog for the test and
// restores the previous package state afterwards. The disk memo is cleared, so
// a cache file present under GOLDER_HOME is still read on the next lookup;
// tests that inject a catalog without a cache file simply leave GOLDER_HOME
// pointing at an empty temp dir (setReasoningCatalogForTest).
func withReasoningCatalog(t *testing.T, providers map[string]map[string]reasoningEntry, checked time.Time) {
	t.Helper()
	reasoningMu.Lock()
	oldCache, oldChecked := reasoningCache, reasoningChecked
	oldSource, oldMod, oldSize := reasoningSource, reasoningMod, reasoningSize
	reasoningCache, reasoningChecked = providers, checked
	reasoningSource, reasoningMod, reasoningSize = "", time.Time{}, 0
	reasoningMu.Unlock()
	t.Cleanup(func() {
		reasoningMu.Lock()
		reasoningCache, reasoningChecked = oldCache, oldChecked
		reasoningSource, reasoningMod, reasoningSize = oldSource, oldMod, oldSize
		reasoningMu.Unlock()
	})
}

// setReasoningCatalogForTest installs a fresh in-process catalog with no disk
// cache behind it (GOLDER_HOME points at an empty temp dir).
func setReasoningCatalogForTest(t *testing.T, providers map[string]map[string]reasoningEntry) {
	t.Helper()
	t.Setenv("GOLDER_HOME", t.TempDir())
	withReasoningCatalog(t, providers, time.Now())
}

// useModelsDevURL points the catalog fetcher at a test server for one test.
func useModelsDevURL(t *testing.T, url string) {
	t.Helper()
	old := modelsDevURL
	modelsDevURL = url
	t.Cleanup(func() { modelsDevURL = old })
}

// TestEnsureReasoningCatalogExtractsCachesAndRefreshes covers the fetch path:
// effort values are extracted (other option types and "none" dropped), only
// registry providers are kept, the result is written to the disk cache, and a
// fresh catalog is never fetched twice.
func TestEnsureReasoningCatalogExtractsCachesAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"opencode-go": {"models": {
				"glm-5.3": {"reasoning": true, "reasoning_options": [
					{"type": "effort", "values": ["low", "none", "max", "high"]},
					{"type": "toggle"}
				]},
				"mimo-v2.6-pro": {"reasoning": true, "reasoning_options": []},
				"plain-1": {"reasoning": false}
			}},
			"unlisted-provider": {"models": {"m": {"reasoning": true, "reasoning_options": [
				{"type": "effort", "values": ["max"]}
			]}}}
		}`)
	}))
	defer srv.Close()
	useModelsDevURL(t, srv.URL)
	withReasoningCatalog(t, nil, time.Time{})

	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("EnsureReasoningCatalog: %v", err)
	}
	if hits != 1 {
		t.Fatalf("fetch hits = %d, want 1", hits)
	}
	// "none" is dropped, and the remaining levels arrive sorted low → high.
	if got, want := ReasoningLevels("opencode-go", "glm-5.3"), []agentcore.ThinkingLevel{
		agentcore.ThinkingLow, agentcore.ThinkingHigh, agentcore.ThinkingMax,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("glm-5.3 levels = %v, want %v", got, want)
	}
	// Reasoning but no effort options: nothing catalog-definite to show.
	if got := KnownReasoningLevels("opencode-go", "mimo-v2.6-pro"); got != nil {
		t.Errorf("mimo levels = %v, want nil (no effort option)", got)
	}
	// Non-registry providers are not decoded into the cache.
	if _, ok := catalogEntry("unlisted-provider", "m"); ok {
		t.Error("unlisted provider leaked into the catalog")
	}
	// The disk cache was written with a check timestamp.
	data, err := os.ReadFile(filepath.Join(dir, reasoningCatalogFileName))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var f reasoningCatalogFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("cache JSON: %v", err)
	}
	if f.CheckedAt.IsZero() || f.Providers["opencode-go"]["glm-5.3"].Levels == nil {
		t.Errorf("cache = %+v, want timestamp + extracted levels", f)
	}
	// A fresh catalog is served from memory: no second fetch.
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("fresh EnsureReasoningCatalog: %v", err)
	}
	if hits != 1 {
		t.Errorf("fresh catalog refetched: hits = %d, want 1", hits)
	}
}

// TestReasoningCatalogTTLDiskLoadAndRefresh covers the cache lifecycle: a
// fresh on-disk cache loads without network, and a stale one triggers a
// refetch that is written back.
func TestReasoningCatalogTTLDiskLoadAndRefresh(t *testing.T) {
	writeCache := func(dir string, checkedAt time.Time, levels []string) {
		t.Helper()
		f := reasoningCatalogFile{
			CheckedAt: checkedAt,
			Providers: map[string]map[string]reasoningEntry{
				"opencode-go": {"m1": {Reasoning: true, Levels: levels}},
			},
		}
		data, _ := json.Marshal(f)
		if err := os.WriteFile(filepath.Join(dir, reasoningCatalogFileName), data, 0o644); err != nil {
			t.Fatalf("write cache: %v", err)
		}
	}

	// Fresh cache: loaded from disk, no network.
	dirFresh := t.TempDir()
	t.Setenv("GOLDER_HOME", dirFresh)
	writeCache(dirFresh, time.Now(), []string{"max"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected fetch: fresh cache must not be refetched")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	useModelsDevURL(t, srv.URL)
	withReasoningCatalog(t, nil, time.Time{})
	if got := ReasoningLevels("opencode-go", "m1"); !reflect.DeepEqual(got, []agentcore.ThinkingLevel{agentcore.ThinkingMax}) {
		t.Fatalf("disk-loaded levels = %v, want [max]", got)
	}
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("fresh EnsureReasoningCatalog: %v", err)
	}

	// Stale cache (its own home dir): the next Ensure refetches and rewrites.
	dirStale := t.TempDir()
	t.Setenv("GOLDER_HOME", dirStale)
	writeCache(dirStale, time.Now().Add(-25*time.Hour), []string{"max"})
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"opencode-go": {"models": {
			"m1": {"reasoning": true, "reasoning_options": [{"type": "effort", "values": ["minimal", "xhigh"]}]}
		}}}`)
	}))
	defer srv2.Close()
	useModelsDevURL(t, srv2.URL)
	withReasoningCatalog(t, nil, time.Time{})
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("stale EnsureReasoningCatalog: %v", err)
	}
	if got, want := ReasoningLevels("opencode-go", "m1"), []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal, agentcore.ThinkingXHigh,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("refreshed levels = %v, want %v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(dirStale, reasoningCatalogFileName))
	if err != nil {
		t.Fatalf("read refreshed cache: %v", err)
	}
	var f reasoningCatalogFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("refreshed cache JSON: %v", err)
	}
	if time.Since(f.CheckedAt) > time.Minute {
		t.Errorf("cache timestamp not refreshed: %v", f.CheckedAt)
	}
	if got := f.Providers["opencode-go"]["m1"].Levels; !reflect.DeepEqual(got, []string{"minimal", "xhigh"}) {
		t.Errorf("cache levels = %v, want [minimal xhigh]", got)
	}
}

// TestReasoningCatalogCorruptAndFetchFailure verifies graceful degradation: a
// corrupt cache never panics (lookups fall back to the family table), and a
// failed refresh surfaces its error without dropping the fallback.
func TestReasoningCatalogCorruptAndFetchFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, reasoningCatalogFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	withReasoningCatalog(t, nil, time.Time{})
	if got := KnownReasoningLevels("opencode-go", "deepseek-v4.1-flash"); got != nil {
		t.Errorf("corrupt cache yielded levels %v, want nil", got)
	}
	want := []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax,
	}
	if got := ReasoningLevels("opencode-go", "deepseek-v4.1-flash"); !reflect.DeepEqual(got, want) {
		t.Errorf("corrupt-cache fallback = %v, want family ladder %v", got, want)
	}

	useModelsDevURL(t, "http://127.0.0.1:9/api.json")
	if err := EnsureReasoningCatalog(context.Background()); err == nil {
		t.Fatal("unreachable catalog endpoint succeeded, want error")
	}
	if got := ReasoningLevels("opencode-go", "deepseek-v4.1-flash"); !reflect.DeepEqual(got, want) {
		t.Errorf("post-failure fallback = %v, want family ladder %v", got, want)
	}
}
