package seatbelt

// Session-level writable roots: paths the user explicitly granted write (and,
// by construction, read) access to from inside the sandbox, without running
// commands unisolated. Two sources feed them at launch — the [permissions]
// table in config.toml and the managed permissions.toml — and the TUI approval
// dialog adds one for the session when a sandbox denial names a path the user
// chooses to re-admit.
//
// The registry is package-level rather than a Runner field because both
// platform runners rebuild their policy per command and every BashTool in the
// process (including task children) shares it; a grant applies to the next
// command without rewiring. It is concurrency-safe: grants can arrive from a
// driver goroutine while run goroutines are building argv.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var writableRoots = struct {
	mu    sync.RWMutex
	paths []string
	seen  map[string]bool
}{seen: map[string]bool{}}

// NormalizeWritableRoot validates one user-supplied root and returns its
// cleaned absolute form. "~/..." expands against the real home directory;
// relative paths are rejected outright (a relative entry would resolve
// differently for every command). The path does not have to exist yet:
// compiler caches such as a fresh CARGO_TARGET_DIR are a common case.
func NormalizeWritableRoot(raw string) (string, error) {
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

// AddWritableRoot grants one path read+write access inside the sandbox for
// this process. It returns the normalized path so callers can echo it back.
func AddWritableRoot(raw string) (string, error) {
	p, err := NormalizeWritableRoot(raw)
	if err != nil {
		return "", err
	}
	writableRoots.mu.Lock()
	defer writableRoots.mu.Unlock()
	if !writableRoots.seen[p] {
		writableRoots.seen[p] = true
		writableRoots.paths = append(writableRoots.paths, p)
	}
	return p, nil
}

// RemoveWritableRoot revokes a path (session only — the persisted list is
// edited by the caller). It reports whether the path was present.
func RemoveWritableRoot(raw string) bool {
	p, err := NormalizeWritableRoot(raw)
	if err != nil {
		return false
	}
	writableRoots.mu.Lock()
	defer writableRoots.mu.Unlock()
	if !writableRoots.seen[p] {
		return false
	}
	delete(writableRoots.seen, p)
	out := writableRoots.paths[:0]
	for _, existing := range writableRoots.paths {
		if existing != p {
			out = append(out, existing)
		}
	}
	writableRoots.paths = out
	return true
}

// WritableRoots returns the current grant list (a copy, safe to retain).
func WritableRoots() []string {
	writableRoots.mu.RLock()
	defer writableRoots.mu.RUnlock()
	if len(writableRoots.paths) == 0 {
		return nil
	}
	out := make([]string, len(writableRoots.paths))
	copy(out, writableRoots.paths)
	return out
}

// SetWritableRoots replaces the whole list: startup applies the config file
// with it, and tests reset state with SetWritableRoots(nil). Invalid entries
// are skipped and reported (nil entries are a programming bug, so they are
// errors too), while every valid one still applies.
func SetWritableRoots(paths []string) []error {
	var errs []error
	var normalized []string
	seen := map[string]bool{}
	for _, raw := range paths {
		p, err := NormalizeWritableRoot(raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !seen[p] {
			seen[p] = true
			normalized = append(normalized, p)
		}
	}
	writableRoots.mu.Lock()
	defer writableRoots.mu.Unlock()
	writableRoots.paths = normalized
	writableRoots.seen = seen
	return errs
}
