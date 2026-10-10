package session

// Project identity for session views (/resume, -c). Every session records the
// directory it ran in (SessionHeader.Cwd); a view anchors itself at the current
// directory and shows the sessions that belong to "the same project". A project
// is the repository containing the directory — the nearest ancestor holding a
// .git entry — so a monorepo keeps one history whether golder was launched at
// the root or three levels down, while a directory outside any repository is
// its own project. Nothing here touches the store: the id is derived from a
// path alone, so it can be compared against already-persisted headers.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// ProjectRoot returns the directory that identifies dir's project: the nearest
// ancestor of dir (dir itself included) holding a .git entry, else dir itself.
//
// The probe is a filesystem stat, not a git subprocess: it costs nothing worth
// caching, it works on a machine without git installed, and it needs no
// accounting for a missing or broken git. Nested checkouts therefore collapse
// to the innermost repository (the submodule boundary), which is also where
// git itself draws the line.
//
// dir is made absolute before the walk, so a relative launch directory matches
// the absolute path a session recorded. An empty or unresolvable dir yields "".
func ProjectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	for d := abs; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs
		}
		d = parent
	}
}

// ProjectID returns a stable 12-hex-character id for the project containing
// dir: the first 12 hex chars of sha256(ProjectRoot(dir)), or "" when dir is
// unresolvable. It is the same shape internal/memory.resolveProjectId derives
// for a project scope, so in-repo both sides agree once they are handed the
// repository root.
func ProjectID(dir string) string {
	root := ProjectRoot(dir)
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:12]
}

// SameProject reports whether directories a and b belong to the same project.
// Two unresolvable paths are never "the same project": attribution requires a
// real directory on both sides.
func SameProject(a, b string) bool {
	ra, rb := ProjectRoot(a), ProjectRoot(b)
	return ra != "" && ra == rb
}
