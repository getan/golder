package grants

// Migration from the three stores this package replaces.
//
// The rule that shapes every line here: a migration may not widen a grant. The
// user answered specific questions in the old world, and those answers have to
// arrive in the new table at exactly the same width — a narrower table is a
// bug the user notices as a new prompt, a wider one is a bug they never see.
//
// So trust.json's `true` becomes Execute, NOT ReadWrite. Trusting a directory
// meant "run side-effect tools here without asking"; it never meant "any tool
// anywhere may rewrite these files". Mapping it to ReadWrite would silently
// hand out a write grant the user never gave — the exact shape of failure this
// design exists to prevent. The file's `false` is carried across as a refusal
// for the same reason: it is a decision the user made, and dropping it would
// make the next run ask again (or worse, proceed).
//
// The legacy files are read and left alone. A user who downgrades golder finds
// their old state intact, and a user who inspects the directory can see where
// each row came from — which is what the Source column records.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/getan/golder/internal/permissions"
)

// LegacyPaths are the stores to read when the unified table appears for the
// first time. Every field may be empty, which skips that source.
type LegacyPaths struct {
	// Trust is $GOLDER_HOME/trust.json: directory → nullable bool.
	Trust string
	// Permissions is ~/.config/golder/permissions.toml: the managed writable
	// and readable roots.
	Permissions string
	// Seed is ~/.config/golder/config.toml, whose [permissions] table is the
	// hand-edited version of the same roots.
	Seed string
}

// LegacyPathsFor resolves the legacy locations from the environment, matching
// the paths the old stores themselves used.
func LegacyPathsFor(getenv func(string) string) LegacyPaths {
	if getenv == nil {
		getenv = os.Getenv
	}
	var out LegacyPaths
	if home := strings.TrimSpace(getenv("GOLDER_HOME")); home != "" {
		out.Trust = filepath.Join(home, "trust.json")
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		out.Trust = filepath.Join(home, ".golder", "trust.json")
	}
	if dir := configDir(getenv); dir != "" {
		out.Permissions = filepath.Join(dir, "permissions.toml")
		out.Seed = filepath.Join(dir, "config.toml")
	}
	return out
}

func configDir(getenv func(string) string) string {
	base := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "golder")
}

// MigrateResult reports what a migration found and did, so the caller can tell
// the user what changed instead of silently rewriting their permissions.
type MigrateResult struct {
	// Imported are the rows the legacy stores produced.
	Imported []Grant
	// Warnings are non-fatal problems: an unreadable or malformed legacy file.
	// Losing an old grant is an inconvenience; refusing to start is not.
	Warnings []string
	// Wrote reports whether the managed file was written.
	Wrote bool
}

// Migrate reads the legacy stores, adds their rows to the table, and persists
// once. It is idempotent and safe to call on every start: it does nothing when
// the managed file already existed or nothing was found, so a user who has
// moved on is not re-migrated from a stale file.
func (s *Store) Migrate(paths LegacyPaths) (MigrateResult, error) {
	var res MigrateResult
	if s.Loaded() {
		// The unified table already exists and wins outright. Re-reading the
		// legacy files would resurrect exactly the grants the user has since
		// revoked here — a downgrade's edits must not climb back up.
		return res, nil
	}

	res.Imported = append(res.Imported, migrateTrust(paths.Trust, &res.Warnings)...)
	res.Imported = append(res.Imported, migratePermissions(paths.Permissions, &res.Warnings)...)
	res.Imported = append(res.Imported, migratePermissions(paths.Seed, &res.Warnings)...)
	if len(res.Imported) == 0 {
		return res, nil
	}

	for _, g := range res.Imported {
		if _, err := s.Add(g); err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("grants: could not import %s: %v", g.Path, err))
		}
	}
	// Add() persists on its own; report whether that succeeded by looking at
	// what the table now holds, rather than assuming.
	res.Wrote = s.Loaded() || s.Path() != ""
	return res, nil
}

// migrateTrust reads trust.json: a map of directory to a nullable bool.
//
//	true  → Allow Execute   (trusting a directory was always about side effects)
//	false → Deny Execute    (an answer the user gave; dropping it would re-ask)
//	null  → nothing         (explicitly undecided: not a decision to carry)
//
// Nothing here grants Read or Write. A directory the user trusted for running
// commands is not a directory they authorised for arbitrary file rewriting, and
// claiming otherwise would be the migration inventing a permission.
func migrateTrust(path string, warn *[]string) []Grant {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		*warn = append(*warn, fmt.Sprintf("grants: cannot read legacy trust store %s: %v", path, err))
		return nil
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var m map[string]*bool
	if err := json.Unmarshal(data, &m); err != nil {
		*warn = append(*warn, fmt.Sprintf("grants: ignoring malformed legacy trust store %s: %v", path, err))
		return nil
	}
	out := make([]Grant, 0, len(m))
	for dir, dec := range m {
		if dec == nil {
			continue
		}
		norm, err := normalizeLegacyPath(dir)
		if err != nil {
			*warn = append(*warn, fmt.Sprintf("grants: skipping trust entry %q: %v", dir, err))
			continue
		}
		g := Grant{
			Path:     norm,
			Scope:    Subtree,
			Lifetime: Remembered,
			Source:   "trust.json",
		}
		if *dec {
			g.Allow = Execute
		} else {
			g.Deny = Execute
		}
		out = append(out, g)
	}
	return out
}

// migratePermissions reads one permissions file: the managed store spells the
// roots at the top level of the document, while config.toml keeps them under
// [permissions]. Both spellings are accepted, so one parser serves both.
//
//	writable_roots → Allow ReadWrite  (a writable root was readable too: the
//	                                   sandbox always admitted it into its read
//	                                   whitelist, because writing a file you
//	                                   cannot read back is a half-failure)
//	readable_roots → Allow Read       (read only, and it must stay read only)
func migratePermissions(path string, warn *[]string) []Grant {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		*warn = append(*warn, fmt.Sprintf("grants: cannot read legacy permissions store %s: %v", path, err))
		return nil
	}
	var f struct {
		Permissions struct {
			WritableRoots []string `toml:"writable_roots"`
			ReadableRoots []string `toml:"readable_roots"`
		} `toml:"permissions"`
		WritableRoots []string `toml:"writable_roots"`
		ReadableRoots []string `toml:"readable_roots"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		*warn = append(*warn, fmt.Sprintf("grants: ignoring malformed legacy permissions store %s: %v", path, err))
		return nil
	}
	source := filepath.Base(path)
	var out []Grant
	for _, raw := range append(f.Permissions.WritableRoots, f.WritableRoots...) {
		if g, ok := legacyRoot(raw, ReadWrite, source, warn); ok {
			out = append(out, g)
		}
	}
	for _, raw := range append(f.Permissions.ReadableRoots, f.ReadableRoots...) {
		if g, ok := legacyRoot(raw, Read, source, warn); ok {
			out = append(out, g)
		}
	}
	return out
}

// legacyRoot turns one legacy root into a row, keeping the capability its list
// meant. The capability is a parameter, never derived from the path: a
// readable root that arrived as read+write would be the migration handing out a
// write grant the user never gave.
func legacyRoot(raw string, caps Capability, source string, warn *[]string) (Grant, bool) {
	norm, err := normalizeLegacyPath(raw)
	if err != nil {
		*warn = append(*warn, fmt.Sprintf("grants: skipping %s entry %q: %v", source, raw, err))
		return Grant{}, false
	}
	return Grant{
		Path:     norm,
		Allow:    caps,
		Scope:    Subtree,
		Lifetime: Remembered,
		Source:   source,
	}, true
}

// normalizeLegacyPath is permissions.NormalizePath with the legacy stores'
// tolerance: an entry that cannot be normalized is skipped with a warning
// rather than failing the migration, because these files were hand-editable and
// a single bad line must not cost the user every other grant.
func normalizeLegacyPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	return permissions.NormalizePath(p)
}
