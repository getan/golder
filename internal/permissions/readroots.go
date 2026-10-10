package permissions

// Readable roots: the directories a user has allowed the read-only tools to
// reach outside the workspace.
//
// The workspace boundary the file tools enforce (resolveWithin in
// internal/agenttool) is a consent boundary rather than a risk boundary: the
// workspace is the directory the user opened, so reading outside it is a
// question to put to the user — not a verdict for the reviewer to make on the
// model's behalf. The gate asks once per directory per session (internal/judge
// ReadScope gate) and the grant lands here, where both the tools (to resolve a
// path) and the sandbox (to widen its read whitelist) can see it.
//
// This parallels the sandbox's writable roots (internal/seatbelt/writable.go)
// and is package-level for the same reason: every tool in the process —
// including a task child's — shares one registry, so a grant applies to the
// next call without rewiring. It is safe for concurrent use: grants arrive from
// a driver goroutine while run goroutines resolve paths.
//
// Deliberately not gated by the approval mode: read-only, ask, auto and
// full-access all ask before crossing the boundary. full-access turns off
// review, not consent, and a run with no prompt available (headless) fails
// closed with guidance instead of guessing.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var readRoots = struct {
	mu    sync.RWMutex
	paths []string
	seen  map[string]bool
}{seen: map[string]bool{}}

// NormalizePath validates one user- or gate-supplied path and returns its
// cleaned absolute form. "~/..." expands against the real home directory;
// relative paths are rejected (they would resolve differently for every
// process). The path need not exist yet.
//
// It is the one path-normalization policy the permission layer has: the
// sandbox's writable roots (seatbelt.NormalizeWritableRoot) delegate here, so
// "~/x" and a relative path mean the same thing to both registries.
func NormalizePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", fmt.Errorf("cannot resolve home directory for %q", raw)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", raw)
	}
	return filepath.Clean(p), nil
}

// Inside reports whether p is root itself or lives under it. Both paths are
// compared as cleaned absolute paths, so a symlinked spelling or a trailing
// separator does not change the answer.
func Inside(root, p string) bool {
	if root == "" || p == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// AbsAgainst returns p as a cleaned absolute path: an absolute p is cleaned, a
// relative one is resolved against base (which defaults to the working
// directory when empty). "~" is deliberately NOT expanded here, because the
// file tools do not expand it either — the gate and the tools must agree on
// what a path names, or the gate would ask about a location the tool never
// visits. User-supplied grants (NormalizeReadRoot) expand it, since there the
// spelling comes from a person rather than from a call argument.
func AbsAgainst(base, p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		raw = "."
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		base = wd
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(absBase, raw)), nil
}

// ReadScopeCandidate returns the directory a boundary question may offer to
// grant for the session, given the file a call asked for: the file's own
// directory, or that directory's parent when the directory itself is too broad
// to hand over. "" means no directory should be offered (the answer is then
// "this one file" or nothing), which happens when both are broad — a file
// sitting directly in the home directory, say.
func ReadScopeCandidate(target string) string {
	if target == "" {
		return ""
	}
	dir := filepath.Dir(filepath.Clean(target))
	if _, broad := BroadReadRoot(dir); !broad {
		return dir
	}
	up := filepath.Dir(dir)
	if up == dir {
		return ""
	}
	if _, broad := BroadReadRoot(up); !broad {
		return up
	}
	return ""
}

// BroadReadRoot reports whether p is too wide to grant. The filesystem root,
// the home directory itself and the directory holding it are refused: a grant
// there is indistinguishable from "no boundary", and a dialog that offers it
// would turn one keystroke into access to everything — the exact rubber stamp
// the boundary exists to prevent. A parent of the workspace is NOT broad: a
// monorepo above the current directory is a real thing to grant.
func BroadReadRoot(p string) (string, bool) {
	cleaned := filepath.Clean(p)
	if cleaned == string(filepath.Separator) {
		return "the filesystem root cannot be granted as readable", true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	home = filepath.Clean(home)
	if cleaned == home {
		return "the home directory itself cannot be granted as readable (grant the directories you need)", true
	}
	// The directory holding $HOME is refused too: on a normal machine that is
	// /Users (or /home), which holds every account's home directory, so
	// granting it is granting everything.
	if parent := filepath.Dir(home); parent != home && cleaned == parent {
		return parent + " holds every user's home and cannot be granted as readable", true
	}
	return "", false
}

// NormalizeReadRoot normalizes one read grant and refuses the roots that would
// amount to removing the boundary.
func NormalizeReadRoot(raw string) (string, error) {
	p, err := NormalizePath(raw)
	if err != nil {
		return "", err
	}
	if reason, bad := BroadReadRoot(p); bad {
		return "", fmt.Errorf("%s", reason)
	}
	return p, nil
}

// AddReadRoot grants one directory for reading, for this session. It returns
// the normalized path so callers can echo it back. The same directory granted
// twice is stored once.
func AddReadRoot(raw string) (string, error) {
	p, err := NormalizeReadRoot(raw)
	if err != nil {
		return "", err
	}
	readRoots.mu.Lock()
	defer readRoots.mu.Unlock()
	if readRoots.seen == nil {
		readRoots.seen = map[string]bool{}
	}
	if !readRoots.seen[p] {
		readRoots.seen[p] = true
		readRoots.paths = append(readRoots.paths, p)
	}
	return p, nil
}

// RemoveReadRoot revokes one grant. It reports whether the path was present.
func RemoveReadRoot(raw string) bool {
	p, err := NormalizeReadRoot(raw)
	if err != nil {
		return false
	}
	readRoots.mu.Lock()
	defer readRoots.mu.Unlock()
	if !readRoots.seen[p] {
		return false
	}
	delete(readRoots.seen, p)
	out := readRoots.paths[:0]
	for _, existing := range readRoots.paths {
		if existing != p {
			out = append(out, existing)
		}
	}
	readRoots.paths = out
	return true
}

// ReadRoots returns the current grant list (a copy, safe to retain).
func ReadRoots() []string {
	readRoots.mu.RLock()
	defer readRoots.mu.RUnlock()
	if len(readRoots.paths) == 0 {
		return nil
	}
	out := make([]string, len(readRoots.paths))
	copy(out, readRoots.paths)
	return out
}

// ReadableAt reports whether p is inside a granted read root. This is the check
// the file tools make once a path has failed the workspace boundary, and the
// one the gate makes to avoid asking twice for the same directory.
func ReadableAt(p string) bool {
	if p == "" {
		return false
	}
	readRoots.mu.RLock()
	defer readRoots.mu.RUnlock()
	for _, root := range readRoots.paths {
		if Inside(root, p) {
			return true
		}
	}
	return false
}

// SetReadRoots replaces the whole list: startup applies the config file and the
// environment with it, and tests reset state with SetReadRoots(nil). Invalid or
// too-broad entries are skipped and reported, while every valid one still
// applies — one typo in config.toml should not cost the rest of the list.
func SetReadRoots(paths []string) []error {
	var errs []error
	var normalized []string
	seen := map[string]bool{}
	for _, raw := range paths {
		p, err := NormalizeReadRoot(raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !seen[p] {
			seen[p] = true
			normalized = append(normalized, p)
		}
	}
	readRoots.mu.Lock()
	defer readRoots.mu.Unlock()
	readRoots.paths = normalized
	readRoots.seen = seen
	return errs
}

// ReadableRootsFromEnv parses GOLDER_READABLE_ROOTS: colon- or comma-separated
// directories the read-only tools may reach without asking, for a machine or a
// script where editing config.toml is not the right place. Entries follow the
// same rules as the config list ("~/..." expands, relative paths and roots too
// broad to be a boundary are dropped), so an entry that would not be accepted
// from the config file is not accepted here either.
//
// It mirrors GOLDER_SANDBOX_READABLE (the sandbox's read whitelist) but is not
// the same knob: that one reaches the OS sandbox only, so a command could read
// a path the file tools still refused. This one is the read-only tools' own
// list, and a grant here also reaches the sandbox, because the read roots feed
// its whitelist too.
func ReadableRootsFromEnv(getenv func(string) string) []string {
	if getenv == nil {
		return nil
	}
	raw := strings.TrimSpace(getenv("GOLDER_READABLE_ROOTS"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ':' || r == ',' }) {
		if _, err := NormalizeReadRoot(part); err == nil {
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}
