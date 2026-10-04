// This file holds the shared disk-cache plumbing for the two model catalogs
// golder keeps under the golder home directory:
//
//   - the models.dev reasoning ladder catalog (reasoning_catalog.go)
//   - the provider model list (modelcatalog.go)
//
// Both are JSON files written opportunistically and read best-effort: a
// missing, unreadable, or corrupt file never surfaces an error, it just means
// "no cache". Both share one freshness window (catalogTTL) so a long-lived
// install refreshes its provider metadata about once a day in total.
package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// catalogTTL is the freshness window shared by every cached catalog. Inside it
// no network refresh happens; after it the next use (or the startup warm-up)
// refetches and rewrites the file.
const catalogTTL = 24 * time.Hour

// catalogFilePath returns the cache path for a catalog file name, or "" when
// the home dir is unavailable. Mirrors selfupdate's cache layout: $GOLDER_HOME
// wins over ~/.golder.
func catalogFilePath(name string) string {
	if dir := os.Getenv("GOLDER_HOME"); dir != "" {
		return filepath.Join(dir, name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".golder", name)
}

// readCatalogFile loads and unmarshals the named catalog into v. It returns
// false (leaving v untouched) when the file is missing, unreadable, or
// corrupt — callers treat that as "no cache".
func readCatalogFile(name string, v any) bool {
	p := catalogFilePath(name)
	if p == "" {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// writeCatalogFile persists v as the named catalog. Failures are silent: the
// cache is an optimization, never a correctness requirement.
func writeCatalogFile(name string, v any) bool {
	p := catalogFilePath(name)
	if p == "" {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false
	}
	data, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return os.WriteFile(p, data, 0o644) == nil
}

// cacheFresh reports whether a cache stamped at checkedAt is still within the
// shared TTL.
func cacheFresh(checkedAt time.Time) bool {
	return !checkedAt.IsZero() && time.Since(checkedAt) < catalogTTL
}
