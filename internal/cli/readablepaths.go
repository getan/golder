package cli

// Readable paths: the shared implementation behind `/permissions readable` in
// both the REPL (a registry Action) and the TUI (the same registry). It is the
// read-side twin of writablepaths.go, and exists because the read grant had no
// persisted form: the approval dialog's "allow reading this folder" lived in
// memory only, while the writable grant beside it had a command that wrote
// permissions.toml. The managed store's schema already carried ReadableRoots
// and startup already read it back — only the writing half was missing.

import (
	"fmt"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/permissions"
)

// Where a readable path comes from; displayed next to the path. The labels are
// the writable side's, since the same two sources feed both.
const (
	ReadableSourceSaved   = WritableSourceSaved
	ReadableSourceSession = WritableSourceSession
)

// ReadablePathEntry is one readable root with its provenance.
type ReadablePathEntry struct {
	Path   string
	Source string
}

// ReadableRemoval reports what a removal touched.
type ReadableRemoval struct {
	Path       string
	WasSession bool
	WasSaved   bool
}

// Found reports whether the path was granted by any source.
func (r ReadableRemoval) Found() bool {
	return r.WasSession || r.WasSaved
}

// ListReadablePaths merges the two files with the live session registry.
// Unreadable entries and malformed files become warnings rather than errors,
// mirroring the writable list.
func ListReadablePaths() ([]ReadablePathEntry, []string) {
	var entries []ReadablePathEntry
	var warnings []string
	seen := map[string]bool{}
	add := func(raw, source string) {
		p, err := permissions.NormalizeReadRoot(raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", source, err))
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		entries = append(entries, ReadablePathEntry{Path: p, Source: source})
	}
	if saved, err := config.LoadPermissionsConfig(); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %v", ReadableSourceSaved, err))
	} else {
		for _, r := range saved.ReadableRoots {
			add(r, ReadableSourceSaved)
		}
	}
	for _, r := range permissions.ReadRoots() {
		add(r, ReadableSourceSession)
	}
	return entries, warnings
}

// AddReadablePath grants a directory for reading: it registers the path for
// this session and appends it to the managed store, so the next launch applies
// it without asking.
func AddReadablePath(raw string) (path string, err error) {
	root, err := permissions.NormalizeReadRoot(raw)
	if err != nil {
		return "", err
	}
	if p, e := permissions.AddReadRoot(root); e == nil {
		root = p
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil {
		return "", err
	}
	found := false
	for _, r := range saved.ReadableRoots {
		if p, e := permissions.NormalizeReadRoot(r); e == nil && p == root {
			found = true
			break
		}
	}
	if !found {
		saved.ReadableRoots = append(saved.ReadableRoots, root)
		if err := config.SavePermissionsConfig(saved); err != nil {
			return "", err
		}
	}
	return root, nil
}

// RemoveReadablePath revokes a path for this session and deletes it from the
// managed store.
func RemoveReadablePath(raw string) (ReadableRemoval, error) {
	root, err := permissions.NormalizeReadRoot(raw)
	if err != nil {
		return ReadableRemoval{}, err
	}
	res := ReadableRemoval{Path: root}
	res.WasSession = permissions.RemoveReadRoot(root)
	saved, err := config.LoadPermissionsConfig()
	if err != nil {
		return res, err
	}
	var kept []string
	for _, r := range saved.ReadableRoots {
		if p, e := permissions.NormalizeReadRoot(r); e == nil && p == root {
			res.WasSaved = true
			continue
		}
		kept = append(kept, r)
	}
	if res.WasSaved {
		saved.ReadableRoots = kept
		if err := config.SavePermissionsConfig(saved); err != nil {
			return res, err
		}
	}
	return res, nil
}

// ReadableSourceLabel renders the provenance of a readable path for the human,
// in the conversation's language.
func ReadableSourceLabel(source string, zh bool) string {
	switch {
	case source == ReadableSourceSaved && zh:
		return "（permissions.toml，已持久化）"
	case source == ReadableSourceSaved:
		return "(permissions.toml, persisted)"
	case zh:
		return "（仅本会话）"
	default:
		return "(this session)"
	}
}
