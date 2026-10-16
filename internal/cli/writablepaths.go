package cli

// Writable paths: the write grants in the authorization table, as the sandbox
// and the write dialogs see them. The read side's twin, over the same table.

import (
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// Where a writable path comes from; displayed next to the path.
const (
	WritableSourceSaved   = ReadableSourceSaved
	WritableSourceSession = ReadableSourceSession
)

// WritablePathEntry is one writable root with its provenance.
type WritablePathEntry struct {
	Path   string
	Source string
	// Detail is the row's own Source field, kept for a caller that wants more
	// than the two-way label.
	Detail string
}

// WritableRemoval reports what a removal touched.
type WritableRemoval struct {
	Path          string
	WasSession    bool
	WasRemembered bool
}

// Found reports whether the path held a write grant at all.
func (r WritableRemoval) Found() bool { return r.WasSession || r.WasRemembered }

// ListWritablePaths returns every row that grants writing, remembered first.
func ListWritablePaths() ([]WritablePathEntry, []string) {
	s := grants.Process()
	if s == nil {
		return nil, nil
	}
	var out []WritablePathEntry
	for _, g := range s.Entries().All() {
		if !g.Allow.Has(grants.Write) {
			continue
		}
		source := WritableSourceSaved
		if g.Lifetime == grants.Session {
			source = WritableSourceSession
		}
		out = append(out, WritablePathEntry{Path: g.Path, Source: source, Detail: g.Source})
	}
	return out, nil
}

// AddWritablePath grants a path read+write inside the sandbox and persists it,
// so the next launch applies it without asking. One row answers both sides,
// because a file you may write and not read back is a half-failure.
func AddWritablePath(raw string) (path string, err error) {
	return grants.AddWritableRoot(raw, grants.Remembered, "writable add")
}

// RemoveWritablePath revokes the write grant a writable root was created for.
//
// It clears read and write together, because that is the row this command put
// there: a writable root carries Read implicitly (a file you may write and not
// read back is a half-failure), so clearing only Write would leave the
// directory readable — the opposite of what "remove the writable path" means.
// An Execute grant on the same row survives, because trust and the boundary are
// separate decisions and this command speaks for the boundary.
func RemoveWritablePath(raw string) (WritableRemoval, error) {
	root, err := permissions.NormalizePath(raw)
	if err != nil {
		return WritableRemoval{}, err
	}
	s := grants.Process()
	if s == nil {
		return WritableRemoval{Path: root}, nil
	}
	rem, err := s.RemoveCapability(root, grants.ReadWrite)
	if err != nil {
		return WritableRemoval{Path: root}, err
	}
	return WritableRemoval{Path: root, WasSession: rem.Session, WasRemembered: rem.Remembered}, nil
}
