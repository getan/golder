package cli

// Sandbox writable paths: the shared implementation behind `/permissions
// writable` in both the REPL (a registry Action) and the TUI (a picker). The
// live session registry lives in internal/seatbelt; the managed store lives in
// internal/cli/config; this file joins the two so list/add/rm behave and report
// identically in either UI.

import (
	"fmt"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/seatbelt"
)

// Where a writable path comes from; displayed next to the path.
const (
	WritableSourceSaved   = "permissions.toml" // the managed store
	WritableSourceSession = "session"          // granted from an approval dialog
)

// WritablePathEntry is one writable root with its provenance. Sources merge in
// the order permissions.toml → session, first label winning, so the list reads
// the same as the startup merge in cmd/golder.
type WritablePathEntry struct {
	Path   string
	Source string
}

// WritableRemoval reports what a removal touched.
type WritableRemoval struct {
	Path       string
	WasSession bool
	WasSaved   bool
}

// Found reports whether the path was granted by any source.
func (r WritableRemoval) Found() bool {
	return r.WasSession || r.WasSaved
}

// ListWritablePaths merges the two files with the live session registry.
// Unreadable entries and malformed files become warnings (returned, not
// printed) rather than errors: the list is a debugging aid and works even
// when half of it is broken.
func ListWritablePaths() ([]WritablePathEntry, []string) {
	var entries []WritablePathEntry
	var warnings []string
	seen := map[string]bool{}
	add := func(raw, source string) {
		p, err := seatbelt.NormalizeWritableRoot(raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", source, err))
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		entries = append(entries, WritablePathEntry{Path: p, Source: source})
	}
	if cfg, err := config.LoadPermissionsConfig(); err != nil {
		warnings = append(warnings, err.Error())
	} else {
		for _, raw := range cfg.WritableRoots {
			add(raw, WritableSourceSaved)
		}
	}
	for _, p := range seatbelt.WritableRoots() {
		if seen[p] {
			continue
		}
		seen[p] = true
		entries = append(entries, WritablePathEntry{Path: p, Source: WritableSourceSession})
	}
	return entries, warnings
}

// AddWritablePath grants a path for this session and persists it to the managed
// store, so it survives restarts. The path is validated first; on a store write
// error nothing is granted (the caller reports and fails closed).
func AddWritablePath(raw string) (path string, err error) {
	root, err := seatbelt.NormalizeWritableRoot(raw)
	if err != nil {
		return "", err
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil {
		return "", err
	}
	found := false
	for _, r := range saved.WritableRoots {
		if p, e := seatbelt.NormalizeWritableRoot(r); e == nil && p == root {
			found = true
			break
		}
	}
	if !found {
		saved.WritableRoots = append(saved.WritableRoots, root)
		if err := config.SavePermissionsConfig(saved); err != nil {
			return "", err
		}
	}
	if _, err := seatbelt.AddWritableRoot(root); err != nil {
		return "", err
	}
	return root, nil
}

// RemoveWritablePath revokes a path for this session and deletes it from the
// managed store.
func RemoveWritablePath(raw string) (WritableRemoval, error) {
	root, err := seatbelt.NormalizeWritableRoot(raw)
	if err != nil {
		return WritableRemoval{}, err
	}
	res := WritableRemoval{Path: root}
	res.WasSession = seatbelt.RemoveWritableRoot(root)
	saved, err := config.LoadPermissionsConfig()
	if err != nil {
		return res, err
	}
	var kept []string
	for _, r := range saved.WritableRoots {
		if p, e := seatbelt.NormalizeWritableRoot(r); e == nil && p == root {
			res.WasSaved = true
			continue
		}
		kept = append(kept, r)
	}
	if res.WasSaved {
		saved.WritableRoots = kept
		if err := config.SavePermissionsConfig(saved); err != nil {
			return res, err
		}
	}
	return res, nil
}

// DisplayHomePath shortens a path under $HOME to "~/…" for display; the grant
// itself always uses the full path. It is ui's implementation, kept under this
// name because every caller in this package (and the TUI's pickers, which share
// the wording) reads it as cli's.
func DisplayHomePath(p string) string { return ui.DisplayHomePath(p) }
