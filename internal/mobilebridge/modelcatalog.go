package mobilebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/provider"
)

// mobileModelCatalogTTL is intentionally longer than the TUI/GUI 24h window:
// phone pickers open frequently, and showing a week-old list beats waiting on
// the network. A stale list is still returned immediately while refresh runs.
const mobileModelCatalogTTL = 7 * 24 * time.Hour

type mobileModelCacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	IDs       []string  `json:"ids"`
}

type mobileModelCacheFile struct {
	Entries map[string]mobileModelCacheEntry `json:"entries"`
}

type mobileModelResult struct {
	IDs    []string `json:"ids"`
	Cached bool     `json:"cached"`
	Fresh  bool     `json:"fresh"`
	Error  string   `json:"error,omitempty"`
}

// ModelsJSON returns a phone cache snapshot immediately when refresh is false,
// or performs a network refresh when refresh is true. A stale cache survives a
// failed refresh and is returned with Error set.
func (s *Service) ModelsJSON(providerName, baseURL, apiKey, cacheDir string, refresh bool) (string, error) {
	spec, ok := provider.LookupProviderSpec(strings.TrimSpace(providerName))
	if !ok || spec.DecisionOnly {
		return "", fmt.Errorf("unknown chat provider %q", providerName)
	}
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = spec.DefaultBaseURL
	}
	key := provider.ModelCatalogCacheKey(spec.Name, base)
	path := filepath.Join(cacheDir, "model-catalog.json")

	var entry mobileModelCacheEntry
	var found bool
	s.modelsMu.Lock()
	found = readMobileModelCache(path, key, &entry)
	s.modelsMu.Unlock()

	if !refresh {
		if !found {
			raw, _ := json.Marshal(mobileModelResult{Cached: false, Fresh: false})
			return string(raw), nil
		}
		raw, _ := json.Marshal(mobileModelResult{
			IDs: entry.IDs, Cached: true,
			Fresh: time.Since(entry.CheckedAt) < mobileModelCatalogTTL,
		})
		return string(raw), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := provider.FetchProviderModels(ctx, spec.Name, base, spec.Protocol, apiKey)
	if err != nil {
		if found {
			raw, _ := json.Marshal(mobileModelResult{
				IDs: entry.IDs, Cached: true, Fresh: false, Error: err.Error(),
			})
			return string(raw), nil
		}
		return "", err
	}
	if len(ids) == 0 {
		return "", errors.New("model discovery returned no models")
	}
	entry = mobileModelCacheEntry{CheckedAt: time.Now().UTC(), IDs: ids}
	s.modelsMu.Lock()
	writeErr := writeMobileModelCache(path, key, entry)
	s.modelsMu.Unlock()
	if writeErr != nil {
		// A cache write failure must not fail the live list the user just fetched.
		// The next refresh will retry the write.
		_ = writeErr
	}
	raw, _ := json.Marshal(mobileModelResult{IDs: ids, Cached: false, Fresh: true})
	return string(raw), nil
}

func readMobileModelCache(path, key string, entry *mobileModelCacheEntry) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var file mobileModelCacheFile
	if json.Unmarshal(raw, &file) != nil || file.Entries == nil {
		return false
	}
	value, ok := file.Entries[key]
	if !ok || len(value.IDs) == 0 {
		return false
	}
	*entry = value
	return true
}

func writeMobileModelCache(path, key string, entry mobileModelCacheEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var file mobileModelCacheFile
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &file)
	}
	if file.Entries == nil {
		file.Entries = make(map[string]mobileModelCacheEntry)
	}
	file.Entries[key] = entry
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
