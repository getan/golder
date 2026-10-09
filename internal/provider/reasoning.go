// This file owns everything about reasoning effort: the mapping from golder's
// unified ThinkingLevel onto each gateway's wire effort value, and the
// models.dev catalog that says which levels a model actually advertises.
//
// The mapping half is pure: no IO, no global state. The catalog half fetches
// and caches models.dev's metadata, because the gateways golder talks to do
// not expose it themselves (opencode-go's /models returns only
// id/object/created/owned_by). They live together because they are one
// decision: WireReasoningEffort reads the catalog and falls back to the
// hand-maintained ladders in this same file.
//
// golder's unified ladder (off < minimal < low < medium < high < xhigh < max)
// is wider than any single gateway's. Every wire driver — Chat Completions
// (`reasoning_effort`) and Responses (`reasoning.effort`) — calls
// WireReasoningEffort, so a model is mapped identically no matter which
// protocol carries its request.
//
// A model's accepted ladder resolves in layers:
//
//  1. the models.dev catalog entry for (provider, model), when it has one,
//  2. the provider's own ReasoningLadders (registry.go), for gateway quirks,
//  3. sharedReasoningLadders, for model families that behave the same on every
//     gateway,
//  4. defaultReasoningLadder, the conservative common denominator.
//
// A requested level above the ladder's top clamps down to its top rung (a
// model with a real max keeps it); a level below its bottom clamps up to the
// bottom rung. off/empty omits the field so the gateway keeps its own default,
// and a catalog model marked as non-reasoning gets no effort field at all.
//
// The catalog is one JSON document keyed by provider. golder extracts only the
// providers it can route to (the built-in registry, using each spec's
// ModelsDevID) and keeps just each model's reasoning flag plus its effort-type
// values, so a full fetch is a few tens of KB on disk. It is cached at
// $GOLDER_HOME/reasoning-catalog.json (or ~/.golder/reasoning-catalog.json)
// with the shared catalogTTL: a session never blocks on the network for it,
// and the fetch happens at most once per TTL instead of once per session.
// Refreshes are conditional (If-None-Match), so an unchanged upstream catalog
// answers 304 and costs no body. Missing, stale, or corrupt caches degrade
// silently to the hand-maintained ladders.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// sharedReasoningLadders is the offline fallback for model families whose
// ladder differs from the default on every gateway. Matching is a
// case-insensitive substring of the model id; the first match wins, so keep
// more specific families above more generic ones. Provider-specific quirks
// belong in the provider's registry entry instead. The catalog supersedes the
// table whenever it has an entry; the table stays for models the catalog is
// missing (and for a first run with no cache).
var sharedReasoningLadders = []ReasoningLadder{
	// DeepSeek's offline fallback is deliberately the full ladder: with no
	// catalog entry (cold cache, unknown model) the requested rung is passed
	// through rather than clamped to a guess. Once models.dev is fetched its
	// advertised set supersedes this table — currently low|high|max for the
	// deepseek models on both the first-party API and opencode-go.
	{"deepseek", []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax,
	}},
}

// defaultReasoningLadder is the conservative common denominator: low/medium/
// high, the rungs both OpenAI wires document. minimal clamps up to low (the
// Responses wire does not list it) and xhigh/max clamp down to high.
var defaultReasoningLadder = []agentcore.ThinkingLevel{
	agentcore.ThinkingLow, agentcore.ThinkingMedium, agentcore.ThinkingHigh,
}

// reasoningRank orders the unified levels for clamping. The zero value is
// unused: off (0) is handled before any lookup.
var reasoningRank = map[agentcore.ThinkingLevel]int{
	agentcore.ThinkingMinimal: 1,
	agentcore.ThinkingLow:     2,
	agentcore.ThinkingMedium:  3,
	agentcore.ThinkingHigh:    4,
	agentcore.ThinkingXHigh:   5,
	agentcore.ThinkingMax:     6,
}

// WireReasoningEffort maps the unified level onto the effort value to send for
// model. Both the Chat Completions and the Responses driver call it, so they
// agree by construction. providerName keys the models.dev catalog; an empty
// providerName simply skips the catalog lookup. An empty return means "omit
// the field": off/unset (or an unknown level) leaves the gateway's default
// behavior untouched, and a catalog model marked non-reasoning is never sent
// an effort at all.
func WireReasoningEffort(providerName, model string, level agentcore.ThinkingLevel) string {
	if level == "" || level == agentcore.ThinkingOff {
		return ""
	}
	rank, ok := reasoningRank[level]
	if !ok {
		return ""
	}

	ladder := familyLadder(providerName, model)
	if e, known := catalogEntry(providerName, model); known {
		if !e.Reasoning {
			return ""
		}
		if lv := parseReasoningLevels(e.Levels); len(lv) > 0 {
			ladder = lv
		}
	}

	// Walk up while the rung is at or below the request; the last one wins, so
	// a request above the ladder's top lands on the top rung and one below its
	// bottom lands on the bottom rung.
	pick := ladder[0]
	for _, rung := range ladder {
		if reasoningRank[rung] > rank {
			break
		}
		pick = rung
	}
	return string(pick)
}

// modelsDevURL is the models.dev catalog endpoint. It is a var so tests can
// point it at an httptest server.
var modelsDevURL = "https://models.dev/api.json"

const (
	// reasoningCatalogFileName is the on-disk cache under the golder home dir.
	reasoningCatalogFileName = "reasoning-catalog.json"
	// reasoningCatalogTimeout bounds one catalog fetch.
	reasoningCatalogTimeout = 20 * time.Second
	// reasoningCatalogVersion is the schema version of the cached file. A
	// cache written by an older golder may lack fields newer code reads (for
	// example Context), so an out-of-date version is treated as "no cache" and
	// refreshed in the background instead of surfacing zero values.
	reasoningCatalogVersion = 2
)

// reasoningEntry is one model's catalog metadata: whether the model reasons at
// all, the effort-type levels it advertises (empty when it reasons but exposes
// no effort control), and its context window in tokens (0 when models.dev does
// not list one). "none" is dropped: golder's off is expressed by omitting the
// wire field, not by an effort value.
type reasoningEntry struct {
	Reasoning bool     `json:"reasoning"`
	Levels    []string `json:"levels,omitempty"`
	// Context is models.dev's limit.context, the model's total context-token
	// budget. It rides the same API response and 24h disk cache the reasoning
	// metadata uses, so context-window lookups cost no extra network request.
	Context int `json:"context,omitempty"`
}

// reasoningCatalogFile is the on-disk shape of the cached catalog.
type reasoningCatalogFile struct {
	Version   int                                  `json:"version"`
	CheckedAt time.Time                            `json:"checked_at"`
	Providers map[string]map[string]reasoningEntry `json:"providers"`
}

var (
	reasoningMu sync.Mutex
	// reasoningCache is the in-memory catalog, nil until a disk read or fetch
	// succeeds. Maps are replaced whole, never mutated in place, so readers may
	// use a snapshot after releasing the lock.
	reasoningCache map[string]map[string]reasoningEntry
	// reasoningChecked is when reasoningCache was read or fetched, for TTL.
	reasoningChecked time.Time
	// reasoningSource / reasoningMod / reasoningSize memoize the disk cache
	// version already loaded, so an unchanged file is not re-read on every
	// lookup while an external rewrite (or a test fixture) is picked up.
	reasoningSource string
	reasoningMod    time.Time
	reasoningSize   int64
	// reasoningStale marks a loaded cache written before
	// reasoningCatalogVersion. Its data is still served best-effort (reasoning
	// levels keep working offline), but it is not considered fresh, so the next
	// lookup or the startup warm-up refreshes it instead of waiting out the TTL
	// for fields the old schema never carried (Context among them).
	reasoningStale bool
)

// loadReasoningCacheLocked refreshes reasoningCache from the disk cache when a
// version not yet loaded is present (memoized by path, mtime, and size). A
// missing or corrupt file leaves the current cache untouched; callers then
// fall back to the family ladder. Callers must hold reasoningMu.
func loadReasoningCacheLocked() {
	p := catalogFilePath(reasoningCatalogFileName)
	if p == "" {
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		return
	}
	if p == reasoningSource && info.ModTime().Equal(reasoningMod) && info.Size() == reasoningSize {
		return
	}
	var f reasoningCatalogFile
	if !readCatalogFile(reasoningCatalogFileName, &f) || f.Providers == nil {
		return
	}
	// Memoize the file version before the schema check so an out-of-date file
	// is not re-read on every lookup while the background refresh replaces it.
	reasoningSource, reasoningMod, reasoningSize = p, info.ModTime(), info.Size()
	reasoningCache = f.Providers
	reasoningChecked = f.CheckedAt
	reasoningStale = f.Version < reasoningCatalogVersion
}

// reasoningCatalogSnapshot returns the current in-memory catalog (loading it
// from disk on first use), or nil when none is available.
func reasoningCatalogSnapshot() map[string]map[string]reasoningEntry {
	reasoningMu.Lock()
	defer reasoningMu.Unlock()
	loadReasoningCacheLocked()
	return reasoningCache
}

// catalogEntry returns the catalog entry for (providerName, model).
func catalogEntry(providerName, model string) (reasoningEntry, bool) {
	data := reasoningCatalogSnapshot()
	if data == nil {
		return reasoningEntry{}, false
	}
	models, ok := data[providerName]
	if !ok {
		return reasoningEntry{}, false
	}
	e, ok := models[model]
	return e, ok
}

// catalogEntryFold is catalogEntry's case-insensitive fallback. models.dev
// keeps vendor casing in model ids ("MiniMax-M2.5", "tencent/Hy3") while many
// gateways list the same models lowercase, so an exact match alone misses
// real entries. The scan is cheap (a provider's map holds a few dozen models)
// and only runs after the exact lookup fails.
func catalogEntryFold(providerName, model string) (reasoningEntry, bool) {
	data := reasoningCatalogSnapshot()
	if data == nil {
		return reasoningEntry{}, false
	}
	models, ok := data[providerName]
	if !ok {
		return reasoningEntry{}, false
	}
	for id, e := range models {
		if strings.EqualFold(id, model) {
			return e, true
		}
	}
	return reasoningEntry{}, false
}

// parseReasoningLevels converts catalog level names to the unified ladder,
// dropping unknown tokens ("none" among them), deduplicating, and ordering low
// to high so display and clamping are deterministic.
func parseReasoningLevels(levels []string) []agentcore.ThinkingLevel {
	out := make([]agentcore.ThinkingLevel, 0, len(levels))
	seen := make(map[agentcore.ThinkingLevel]struct{}, len(levels))
	for _, s := range levels {
		lvl := agentcore.ThinkingLevel(s)
		if _, ok := reasoningRank[lvl]; !ok {
			continue
		}
		if _, dup := seen[lvl]; dup {
			continue
		}
		seen[lvl] = struct{}{}
		out = append(out, lvl)
	}
	sort.Slice(out, func(i, j int) bool { return reasoningRank[out[i]] < reasoningRank[out[j]] })
	if len(out) == 0 {
		return nil
	}
	return out
}

// familyLadder returns the hand-maintained ladder for a model: the provider's
// own registry overrides first, then the shared model-family table, then the
// conservative default.
func familyLadder(providerName, model string) []agentcore.ThinkingLevel {
	id := strings.ToLower(model)
	if spec, ok := LookupProviderSpec(providerName); ok {
		for _, rule := range spec.ReasoningLadders {
			if strings.Contains(id, rule.Match) {
				return rule.Ladder
			}
		}
	}
	for _, rule := range sharedReasoningLadders {
		if strings.Contains(id, rule.Match) {
			return rule.Ladder
		}
	}
	return defaultReasoningLadder
}

// ReasoningLevels returns the selectable reasoning levels for a model: the
// models.dev catalog's effort list when it has one, else the family ladder.
// An empty (nil) return means the catalog says the model does not reason, so
// there is nothing to select and the wire sends no effort field. The result is
// never mutated; callers must not modify it.
func ReasoningLevels(providerName, model string) []agentcore.ThinkingLevel {
	if e, ok := catalogEntry(providerName, model); ok {
		if !e.Reasoning {
			return nil
		}
		if lv := parseReasoningLevels(e.Levels); len(lv) > 0 {
			return lv
		}
	}
	return familyLadder(providerName, model)
}

// KnownReasoningLevels returns the catalog's explicit effort list for a model,
// or nil when the catalog has nothing definitive (unknown model, no effort
// option, or no reasoning). Unlike ReasoningLevels it never falls back to the
// family table, so callers can tell "models.dev says so" apart from "unknown"
// when annotating UI.
func KnownReasoningLevels(providerName, model string) []agentcore.ThinkingLevel {
	e, ok := catalogEntry(providerName, model)
	if !ok || !e.Reasoning {
		return nil
	}
	return parseReasoningLevels(e.Levels)
}

// ContextWindowFor returns the context-token budget models.dev lists for
// (providerName, model), or 0 when it has no value. Lookups read the reasoning
// catalog's in-memory/disk copy, so they never block on the network; callers
// fall back to cli.DefaultContextWindow on 0 (unknown provider, custom
// base-URL model, or a catalog that has not been fetched yet). A case-only
// mismatch between the gateway's model id and models.dev's brand casing still
// matches.
func ContextWindowFor(providerName, model string) int {
	if providerName == "" || model == "" {
		return 0
	}
	e, ok := catalogEntry(providerName, model)
	if !ok {
		e, ok = catalogEntryFold(providerName, model)
		if !ok {
			return 0
		}
	}
	return e.Context
}

// EnsureReasoningCatalog refreshes the catalog when the in-memory or on-disk
// copy is missing or older than the TTL, and loads it into memory. Concurrent
// callers serialize; the loser of the race re-checks freshness and returns
// without a second fetch. Network errors leave the existing cache untouched.
func EnsureReasoningCatalog(ctx context.Context) error {
	reasoningMu.Lock()
	defer reasoningMu.Unlock()
	loadReasoningCacheLocked()
	if reasoningCache != nil && cacheFresh(reasoningChecked) && !reasoningStale {
		return nil
	}
	// Only a cache written by the current schema may answer a conditional
	// request: a 304 on an old-schema trim would "confirm" upstream bytes
	// whose new fields the old trim never extracted (Context among them).
	cond := ""
	if reasoningCache != nil && !reasoningStale {
		cond = readCatalogETag(reasoningCatalogFileName)
	}
	providers, etag, err := fetchModelsDevCatalog(ctx, cond)
	if errors.Is(err, errCatalogNotModified) {
		// Upstream is unchanged: keep the trimmed data, move the freshness
		// stamp forward so the next refresh is another TTL away.
		reasoningChecked = time.Now()
		writeReasoningCache(reasoningCatalogFile{Version: reasoningCatalogVersion, CheckedAt: reasoningChecked, Providers: reasoningCache})
		return nil
	}
	if err != nil {
		return err
	}
	reasoningCache = providers
	reasoningChecked = time.Now()
	reasoningStale = false
	writeReasoningCache(reasoningCatalogFile{Version: reasoningCatalogVersion, CheckedAt: reasoningChecked, Providers: providers})
	writeCatalogETag(reasoningCatalogFileName, etag)
	return nil
}

// StartBackgroundReasoningCatalogRefresh refreshes a stale or missing catalog
// in a goroutine so startup never blocks on models.dev. It returns immediately
// and swallows errors: a failed refresh leaves the previous cache in place.
func StartBackgroundReasoningCatalogRefresh() {
	reasoningMu.Lock()
	loadReasoningCacheLocked()
	fresh := reasoningCache != nil && cacheFresh(reasoningChecked) && !reasoningStale
	reasoningMu.Unlock()
	if fresh {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reasoningCatalogTimeout+10*time.Second)
		defer cancel()
		_ = EnsureReasoningCatalog(ctx)
	}()
}

// writeReasoningCache persists the catalog and records the file version so the
// next load skips re-reading it. Failures are silent.
func writeReasoningCache(f reasoningCatalogFile) {
	if !writeCatalogFile(reasoningCatalogFileName, f) {
		return
	}
	p := catalogFilePath(reasoningCatalogFileName)
	if p == "" {
		return
	}
	if info, err := os.Stat(p); err == nil {
		reasoningSource, reasoningMod, reasoningSize = p, info.ModTime(), info.Size()
	}
}

// modelsDevProvider is the subset of a models.dev provider entry golder reads.
type modelsDevProvider struct {
	Models map[string]struct {
		Reasoning        bool `json:"reasoning"`
		ReasoningOptions []struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"reasoning_options"`
		Limit struct {
			Context int `json:"context"`
		} `json:"limit"`
	} `json:"models"`
}

// errCatalogNotModified reports a 304 to a conditional models.dev fetch: the
// cached trim is still current upstream, so callers only need to refresh the
// freshness stamp.
var errCatalogNotModified = errors.New("reasoning catalog not modified")

// fetchModelsDevCatalog downloads models.dev's api.json and extracts the
// reasoning metadata of the built-in providers' models. Providers outside the
// registry are not decoded at all, keeping a full fetch cheap. ifNoneMatch,
// when non-empty, makes the request conditional; a 304 answer is reported as
// errCatalogNotModified, and the returned ETag is the one to record for the
// next refresh (empty when the server sends none).
func fetchModelsDevCatalog(ctx context.Context, ifNoneMatch string) (map[string]map[string]reasoningEntry, string, error) {
	ctx, cancel := context.WithTimeout(ctx, reasoningCatalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("reasoning catalog: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := (&http.Client{Timeout: reasoningCatalogTimeout}).Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("reasoning catalog request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && ifNoneMatch != "" {
		return nil, "", errCatalogNotModified
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("reasoning catalog: GET %s returned %d", modelsDevURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", fmt.Errorf("reasoning catalog read failed: %w", err)
	}
	// Decode the top level as raw messages so only registry providers are
	// fully parsed (api.json is several MB; most of it is irrelevant).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, "", fmt.Errorf("reasoning catalog: unexpected response shape: %w", err)
	}
	out := make(map[string]map[string]reasoningEntry, len(providerRegistry))
	for _, spec := range providerRegistry {
		key := spec.ModelsDevID
		if key == "" {
			key = spec.Name
		}
		raw, ok := top[key]
		if !ok {
			continue
		}
		var prov modelsDevProvider
		if err := json.Unmarshal(raw, &prov); err != nil {
			continue
		}
		models := make(map[string]reasoningEntry, len(prov.Models))
		for id, m := range prov.Models {
			e := reasoningEntry{Reasoning: m.Reasoning, Context: m.Limit.Context}
			for _, opt := range m.ReasoningOptions {
				if opt.Type != "effort" {
					continue
				}
				for _, v := range opt.Values {
					e.Levels = append(e.Levels, v)
				}
			}
			models[id] = e
		}
		if len(models) > 0 {
			out[spec.Name] = models
		}
	}
	return out, resp.Header.Get("ETag"), nil
}
