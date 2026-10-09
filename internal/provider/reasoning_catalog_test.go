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
	"sync"
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
	oldStale := reasoningStale
	reasoningCache, reasoningChecked = providers, checked
	reasoningSource, reasoningMod, reasoningSize = "", time.Time{}, 0
	reasoningStale = false
	reasoningMu.Unlock()
	t.Cleanup(func() {
		reasoningMu.Lock()
		reasoningCache, reasoningChecked = oldCache, oldChecked
		reasoningSource, reasoningMod, reasoningSize = oldSource, oldMod, oldSize
		reasoningStale = oldStale
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
				"glm-5.3": {"reasoning": true, "limit": {"context": 200000}, "reasoning_options": [
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
	// limit.context rides the same extraction: the window is served from the
	// catalog without a second fetch.
	if got := ContextWindowFor("opencode-go", "glm-5.3"); got != 200000 {
		t.Errorf("glm-5.3 context window = %d, want 200000", got)
	}
	if got := ContextWindowFor("opencode-go", "plain-1"); got != 0 {
		t.Errorf("model without limit.context = %d, want 0", got)
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
	if f.Version != reasoningCatalogVersion {
		t.Errorf("cache version = %d, want %d", f.Version, reasoningCatalogVersion)
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
			Version:   reasoningCatalogVersion,
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

// TestContextWindowForNeverBlocksOnRefresh pins the startup contract: lookups
// read the catalog without waiting for an in-flight network refresh. main
// kicks off the background models.dev fetch and the TUI/REPL seeds resolve
// their window right after, so if lookups serialized on the fetch, startup
// stalled for the whole 5.4 MB download (or its 20s timeout) — the exact
// "takes ages to start" regression. The lookup must see the previous
// snapshot and return promptly; the refreshed value lands afterwards. The
// 1s budget is the startup-path guarantee: the blocking shape waited for the
// whole fetch (unbounded here), while a healthy lookup is a map read.
func TestContextWindowForNeverBlocksOnRefresh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		_, _ = io.WriteString(w, `{"opencode-go": {"models": {"m1": {"reasoning": true, "limit": {"context": 123}}}}}`)
	}))
	defer srv.Close()
	// Deferred after srv.Close on purpose: LIFO means releaseNow runs first, so
	// a failing assertion cannot leave the handler blocked while Close waits.
	defer releaseNow()
	useModelsDevURL(t, srv.URL)

	// A stale cache so the refresh actually hits the network.
	withReasoningCatalog(t, map[string]map[string]reasoningEntry{
		"opencode-go": {"m1": {Reasoning: true, Context: 7}},
	}, time.Now().Add(-2*catalogTTL))

	done := make(chan error, 1)
	go func() { done <- EnsureReasoningCatalog(context.Background()) }()
	<-started // the fetch is in flight and holding whatever lock it takes

	got := make(chan int, 1)
	go func() { got <- ContextWindowFor("opencode-go", "m1") }()
	select {
	case v := <-got:
		if v != 7 {
			t.Fatalf("window during refresh = %d, want the previous snapshot's 7", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ContextWindowFor blocked for over 1s while a catalog refresh was in flight (startup budget)")
	}

	releaseNow()
	if err := <-done; err != nil {
		t.Fatalf("EnsureReasoningCatalog: %v", err)
	}
	if v := ContextWindowFor("opencode-go", "m1"); v != 123 {
		t.Fatalf("window after refresh = %d, want 123", v)
	}
}

// TestReasoningCatalogConditionalRequest pins the ETag path: the first fetch
// records the server's ETag, a later refresh sends it as If-None-Match, and a
// 304 keeps the trimmed data while moving the freshness stamp forward.
func TestReasoningCatalogConditionalRequest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	full, conditional := 0, 0
	var noneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			conditional++
			noneMatch = inm
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full++
		w.Header().Set("ETag", `W/"cat-1"`)
		_, _ = io.WriteString(w, `{"opencode-go": {"models": {"m1": {"reasoning": true, "limit": {"context": 123456}}}}}`)
	}))
	defer srv.Close()
	useModelsDevURL(t, srv.URL)
	withReasoningCatalog(t, nil, time.Time{})

	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("first EnsureReasoningCatalog: %v", err)
	}
	if got := ContextWindowFor("opencode-go", "m1"); got != 123456 {
		t.Fatalf("window = %d, want 123456", got)
	}
	if got := readCatalogETag(reasoningCatalogFileName); got != `W/"cat-1"` {
		t.Fatalf("recorded etag = %q, want W/\"cat-1\"", got)
	}

	// Age the in-memory stamp: the next Ensure must refresh, and the recorded
	// ETag must turn that into a conditional request.
	reasoningMu.Lock()
	reasoningChecked = time.Now().Add(-2 * catalogTTL)
	reasoningMu.Unlock()
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("conditional EnsureReasoningCatalog: %v", err)
	}
	if conditional != 1 || full != 1 {
		t.Fatalf("requests: full = %d, conditional = %d; want 1 and 1", full, conditional)
	}
	if noneMatch != `W/"cat-1"` {
		t.Errorf("If-None-Match = %q, want W/\"cat-1\"", noneMatch)
	}
	// The 304 kept the trimmed data and pushed the freshness stamp out, so the
	// next call within the TTL does not touch the network at all.
	if got := ContextWindowFor("opencode-go", "m1"); got != 123456 {
		t.Errorf("window after 304 = %d, want 123456", got)
	}
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("post-304 EnsureReasoningCatalog: %v", err)
	}
	if full+conditional != 2 {
		t.Errorf("requests after fresh stamp = %d, want 2", full+conditional)
	}
}

// TestReasoningCatalogSkipsConditionalOnStaleSchema pins the safety rule: a
// cache written by an older schema must not send If-None-Match — a 304 would
// confirm upstream bytes whose new fields that trim never extracted.
func TestReasoningCatalogSkipsConditionalOnStaleSchema(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	old := `{"checked_at":"` + time.Now().UTC().Format(time.RFC3339) +
		`","providers":{"opencode-go":{"m1":{"reasoning":true}}}}`
	if err := os.WriteFile(filepath.Join(dir, reasoningCatalogFileName), []byte(old), 0o644); err != nil {
		t.Fatalf("write old-schema cache: %v", err)
	}
	writeCatalogETag(reasoningCatalogFileName, `W/"cat-1"`)

	sentConditional := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			sentConditional = true
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"cat-2"`)
		_, _ = io.WriteString(w, `{"opencode-go": {"models": {"m1": {"reasoning": true, "limit": {"context": 777}}}}}`)
	}))
	defer srv.Close()
	useModelsDevURL(t, srv.URL)
	withReasoningCatalog(t, nil, time.Time{})

	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("EnsureReasoningCatalog: %v", err)
	}
	if sentConditional {
		t.Fatal("old-schema cache sent If-None-Match; a 304 would skip fields it lacks")
	}
	if got := ContextWindowFor("opencode-go", "m1"); got != 777 {
		t.Errorf("window after schema refresh = %d, want 777", got)
	}
	if got := readCatalogETag(reasoningCatalogFileName); got != `W/"cat-2"` {
		t.Errorf("etag after refresh = %q, want W/\"cat-2\"", got)
	}
}

// TestContextWindowFor pins the limit.context lookup: the catalog value is
// returned for a known model, and 0 for an unknown provider or model and for
// entries models.dev lists without a limit.
func TestContextWindowFor(t *testing.T) {
	setReasoningCatalogForTest(t, map[string]map[string]reasoningEntry{
		"anthropic": {
			"claude-sonnet-4-5": {Reasoning: true, Context: 200000},
			"no-limit":          {Reasoning: false},
		},
		"minimax": {
			// models.dev keeps vendor casing; gateways list this lowercase.
			"MiniMax-M2.5": {Reasoning: true, Context: 1000000},
		},
	})
	if got := ContextWindowFor("anthropic", "claude-sonnet-4-5"); got != 200000 {
		t.Errorf("known window = %d, want 200000", got)
	}
	if got := ContextWindowFor("minimax", "minimax-m2.5"); got != 1000000 {
		t.Errorf("case-folded window = %d, want 1000000", got)
	}
	if got := ContextWindowFor("anthropic", "no-limit"); got != 0 {
		t.Errorf("entry without limit = %d, want 0", got)
	}
	if got := ContextWindowFor("anthropic", "unknown"); got != 0 {
		t.Errorf("unknown model = %d, want 0", got)
	}
	if got := ContextWindowFor("unknown-provider", "m"); got != 0 {
		t.Errorf("unknown provider = %d, want 0", got)
	}
	if got := ContextWindowFor("", "m"); got != 0 {
		t.Errorf("empty provider = %d, want 0", got)
	}
}

// TestReasoningCatalogSchemaVersionGate verifies a cache written before Context
// existed still serves its reasoning levels (so an offline user loses nothing),
// reads as an unknown window rather than a zero-token one, and is refreshed by
// the next Ensure instead of waiting out the TTL.
func TestReasoningCatalogSchemaVersionGate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	// Version 1 shape: no "version" field, no per-model context.
	old := `{"checked_at":"` + time.Now().UTC().Format(time.RFC3339) +
		`","providers":{"opencode-go":{"m1":{"reasoning":true,"levels":["max"]}}}}`
	if err := os.WriteFile(filepath.Join(dir, reasoningCatalogFileName), []byte(old), 0o644); err != nil {
		t.Fatalf("write old-schema cache: %v", err)
	}
	withReasoningCatalog(t, nil, time.Time{})
	if got := ReasoningLevels("opencode-go", "m1"); !reflect.DeepEqual(got, []agentcore.ThinkingLevel{agentcore.ThinkingMax}) {
		t.Fatalf("old-schema cache should still serve levels, got %v", got)
	}
	if got := ContextWindowFor("opencode-go", "m1"); got != 0 {
		t.Fatalf("old-schema cache served window %d, want 0", got)
	}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"opencode-go": {"models": {"m1": {"reasoning": true, "limit": {"context": 123456}}}}}`)
	}))
	defer srv.Close()
	useModelsDevURL(t, srv.URL)
	if err := EnsureReasoningCatalog(context.Background()); err != nil {
		t.Fatalf("EnsureReasoningCatalog after version gate: %v", err)
	}
	if hits != 1 {
		t.Fatalf("old-schema cache did not trigger a refetch: hits = %d, want 1", hits)
	}
	if got := ContextWindowFor("opencode-go", "m1"); got != 123456 {
		t.Errorf("window after refetch = %d, want 123456", got)
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
