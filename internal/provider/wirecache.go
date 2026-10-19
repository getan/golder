package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/golderhome"
)

// The confirmed wire lives in two layers: an in-process map, and one JSON file
// under GOLDER_HOME so a restart does not pay for the same answer again. An
// answer is keyed by (provider, model) and, once confirmed, also by
// (provider, "family:"+family): a gateway routes one line the same way, so the
// first model asked answers for its siblings (see ModelFamily).
//
// An entry also records where the answer came from, because the two sources are
// not worth the same. A real request carried by a wire (the adaptive fallback,
// fallback.go) is evidence about this gateway and may overturn the shared family
// table; a probe's 1-token greeting may not (see cachedWireFor).
//
// Entries expire after wireCacheTTL (one month): a gateway can change what it
// serves, and a stale answer would silently pin the wrong wire. An expired entry
// is treated as absent and re-probed in the background on the next resolution.

const wireCacheTTL = 30 * 24 * time.Hour

type wireCacheEntry struct {
	Wire Wire      `json:"wire"`
	At   time.Time `json:"at"`
	// Served marks the answer a wire gave a real request, as opposed to one a
	// probe reported. Entries written before this field existed read as
	// probe-grade, which is the conservative side: an old answer is re-learned by
	// the first turn that needs it rather than trusted for a month.
	Served bool `json:"served,omitempty"`
}

var (
	wireCacheOnce sync.Once
	wireCacheMu   sync.Mutex
	wireCache     map[string]wireCacheEntry
)

func wireCachePath() string {
	return filepath.Join(golderhome.Dir(), "wire-cache.json")
}

func loadWireCache() {
	wireCacheOnce.Do(func() {
		wireCache = map[string]wireCacheEntry{}
		raw, err := os.ReadFile(wireCachePath())
		if err != nil {
			return
		}
		_ = json.Unmarshal(raw, &wireCache)
	})
}

func wireCacheKey(provider, modelOrFamily string) string {
	return provider + "\x00" + strings.ToLower(strings.TrimSpace(modelOrFamily))
}

func lookupWire(provider, modelOrFamily string) (Wire, bool, bool) {
	loadWireCache()
	wireCacheMu.Lock()
	entry, ok := wireCache[wireCacheKey(provider, modelOrFamily)]
	wireCacheMu.Unlock()
	if !ok || entry.Wire == "" || time.Since(entry.At) > wireCacheTTL {
		return "", false, false
	}
	return entry.Wire, entry.Served, true
}

// storeWire records a probe's answer: the wire an endpoint claimed for a model
// when asked a question — not when asked to carry a turn. It spares the next
// resolution the question; see cachedWireFor for what such an answer may stand
// in for.
func storeWire(provider, modelOrFamily string, w Wire) {
	storeWireEntry(provider, modelOrFamily, w, false)
}

// storeServedWire records an answer a real request produced: the adaptive
// fallback watched this wire carry the model's own turn (or answer with the
// model's own error rather than a protocol rejection). This is the strongest
// answer the cache holds, and the only kind allowed to overturn the shared
// family table.
func storeServedWire(provider, modelOrFamily string, w Wire) {
	storeWireEntry(provider, modelOrFamily, w, true)
}

func storeWireEntry(provider, modelOrFamily string, w Wire, served bool) {
	loadWireCache()
	wireCacheMu.Lock()
	defer wireCacheMu.Unlock()
	wireCache[wireCacheKey(provider, modelOrFamily)] = wireCacheEntry{Wire: w, At: time.Now(), Served: served}
	raw, err := json.MarshalIndent(wireCache, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(golderhome.Dir(), 0o755); err != nil {
		return
	}
	tmp := wireCachePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, wireCachePath())
}

// CachedWire returns the wire confirmed for (provider, model) — by the model
// itself, or by its family — from the process map or the persisted file, and
// whether a real request produced that answer (see storeServedWire).
func CachedWire(providerName, model string) (Wire, bool, bool) {
	if w, served, ok := lookupWire(providerName, model); ok {
		return w, served, true
	}
	if fam := ModelFamily(model); fam != "" {
		return lookupWire(providerName, "family:"+fam)
	}
	return "", false, false
}
