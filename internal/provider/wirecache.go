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
// Entries expire after wireCacheTTL (one month): a gateway can change what it
// serves, and a stale answer would silently pin the wrong wire. An expired entry
// is treated as absent and re-probed in the background on the next resolution.

const wireCacheTTL = 30 * 24 * time.Hour

type wireCacheEntry struct {
	Wire Wire      `json:"wire"`
	At   time.Time `json:"at"`
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

func lookupWire(provider, modelOrFamily string) (Wire, bool) {
	loadWireCache()
	wireCacheMu.Lock()
	entry, ok := wireCache[wireCacheKey(provider, modelOrFamily)]
	wireCacheMu.Unlock()
	if !ok || entry.Wire == "" || time.Since(entry.At) > wireCacheTTL {
		return "", false
	}
	return entry.Wire, true
}

func storeWire(provider, modelOrFamily string, w Wire) {
	loadWireCache()
	wireCacheMu.Lock()
	defer wireCacheMu.Unlock()
	wireCache[wireCacheKey(provider, modelOrFamily)] = wireCacheEntry{Wire: w, At: time.Now()}
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
// itself, or by its family — from the process map or the persisted file.
func CachedWire(providerName, model string) (Wire, bool) {
	if w, ok := lookupWire(providerName, model); ok {
		return w, true
	}
	if fam := ModelFamily(model); fam != "" {
		return lookupWire(providerName, "family:"+fam)
	}
	return "", false
}
