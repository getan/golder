// This file persists the provider model lists fetched by /model (issue #566),
// so the list is requested about once per catalogTTL (24h) per provider and
// endpoint instead of once per session. It shares the file plumbing and the
// TTL with the reasoning ladder catalog (catalogcache.go), keeping the two
// caches one mechanism.
//
// A cache key is the provider name plus the effective base URL, so a
// --base-url override (or a self-hosted endpoint) never serves another
// endpoint's list. Entries are independent: each carries its own checked_at.
package provider

import (
	"strings"
	"sync"
	"time"
)

// modelCatalogFileName is the on-disk cache under the golder home dir.
const modelCatalogFileName = "model-catalog.json"

// modelCatalogEntry is one provider+endpoint's cached model list.
type modelCatalogEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	IDs       []string  `json:"ids"`
}

// modelCatalogFile is the on-disk shape: every fetched catalog in one file.
type modelCatalogFile struct {
	Entries map[string]modelCatalogEntry `json:"entries"`
}

// modelCatalogMu serializes the read-modify-write of the shared file. The
// in-process callers are the REPL/TUI goroutines; cross-process writers are
// best-effort (last write wins), matching selfupdate's cache behavior.
var modelCatalogMu sync.Mutex

// ModelCatalogCacheKey identifies a model catalog by provider and effective
// endpoint. The base URL is normalized (trimmed, trailing slash dropped) so
// the same endpoint always maps to one entry.
func ModelCatalogCacheKey(providerName, baseURL string) string {
	providerName = strings.TrimSpace(providerName)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return providerName
	}
	return providerName + "|" + baseURL
}

// CachedModelCatalog returns the cached list for key and whether it is still
// fresh (younger than catalogTTL). A nil list with fresh=false means "no
// usable cache": either nothing was ever stored, or the stored entry is stale
// (callers may still use the list as a fallback when a refetch fails).
func CachedModelCatalog(key string) (ids []string, fresh bool) {
	if strings.TrimSpace(key) == "" {
		return nil, false
	}
	modelCatalogMu.Lock()
	defer modelCatalogMu.Unlock()
	var f modelCatalogFile
	if !readCatalogFile(modelCatalogFileName, &f) {
		return nil, false
	}
	e, ok := f.Entries[key]
	if !ok || len(e.IDs) == 0 {
		return nil, false
	}
	return e.IDs, cacheFresh(e.CheckedAt)
}

// StoreModelCatalog records a freshly fetched list for key.
func StoreModelCatalog(key string, ids []string) {
	if strings.TrimSpace(key) == "" || len(ids) == 0 {
		return
	}
	modelCatalogMu.Lock()
	defer modelCatalogMu.Unlock()
	var f modelCatalogFile
	readCatalogFile(modelCatalogFileName, &f)
	if f.Entries == nil {
		f.Entries = make(map[string]modelCatalogEntry)
	}
	f.Entries[key] = modelCatalogEntry{CheckedAt: time.Now(), IDs: ids}
	writeCatalogFile(modelCatalogFileName, f)
}
