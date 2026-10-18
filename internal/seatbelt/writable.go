package seatbelt

// The writable roots the sandbox profile admits, beyond the project and the
// temp directory.
//
// The grants themselves live in the authorization table (internal/grants),
// which is the one store every layer reads: /permissions, the approval
// dialogs, the boundary gate and this package's own profile builders. What
// stays here is the sandbox's view of them — a delegation, not a cache, so a
// grant the user gives mid-session applies to the next command with no
// rewiring, and no second copy exists to drift.

import (
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// NormalizeWritableRoot validates one user-supplied root and returns its
// cleaned absolute form. "~/..." expands against the real home directory;
// relative paths are rejected outright (a relative entry would resolve
// differently for every command). The path does not have to exist yet:
// compiler caches such as a fresh CARGO_TARGET_DIR are a common case.
//
// It delegates to permissions.NormalizePath so the write side and the read
// side (permissions.NormalizeReadRoot) agree on what a path spelling means.
func NormalizeWritableRoot(raw string) (string, error) {
	return permissions.NormalizePath(raw)
}

// WritableRoots returns the directory grants the profile may write, in the
// form both platform runners want (subtree roots, sorted by the table).
func WritableRoots() []string { return grants.WritableRoots() }

// readGrantsInsideDenies returns the table's effective read grants that lie
// strictly inside one of denyDirs. A deny rule is emitted after the broad
// allows (later rules win), so a more specific grant under a denied subtree
// must be re-asserted after the deny to match the table's most-specific-wins
// decision.
func readGrantsInsideDenies(denyDirs []string) []string {
	return specificGrantsInsideDenies(grants.ReadRoots(), grants.Read, denyDirs)
}

// writeGrantsInsideDenies is readGrantsInsideDenies for the write side.
func writeGrantsInsideDenies(denyDirs []string) []string {
	return specificGrantsInsideDenies(grants.WritableRoots(), grants.Write, denyDirs)
}

// specificGrantsInsideDenies returns the effective user grants that are
// strictly inside one of denyDirs. A grant that is a parent of the deny is
// deliberately not returned: re-asserting it would re-open the whole subtree
// the refusal singled out, and the table itself would still deny the child
// because the more specific row wins.
func specificGrantsInsideDenies(roots []string, cap grants.Capability, denyDirs []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, root := range roots {
		if !grants.Allows(root, cap) {
			continue
		}
		for _, deny := range denyDirs {
			if deny == "" || root == "" || deny == root {
				continue
			}
			if permissions.Inside(deny, root) {
				if !seen[root] {
					seen[root] = true
					out = append(out, root)
				}
				break
			}
		}
	}
	return out
}
