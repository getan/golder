package cli

// Readable paths: the read grants in the authorization table, as the boundary
// gate and the read tools see them. Shared by the REPL and the TUI so both
// front-ends list and edit the same rows in the same words.
//
// There used to be a second store beside the table — permissions.toml, which
// the dialogs wrote and startup read back — and these functions merged the two.
// The table is now the only one, so the merge is gone: a row IS the grant, and
// what a person adds here is what every layer reads on the next call.

import (
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// Where a readable path comes from; displayed next to the path. The remembered
// label names the file the row lives in, and the session label marks what dies
// with the process.
const (
	ReadableSourceSaved   = "grants.json"
	ReadableSourceSession = "session"
)

// ReadablePathEntry is one readable root with its provenance.
type ReadablePathEntry struct {
	Path   string
	Source string
	// Detail is the row's own Source field (which dialog or command recorded
	// it), kept for a caller that wants to show more than the two-way label.
	Detail string
}

// ReadableRemoval reports what a removal touched.
type ReadableRemoval struct {
	Path          string
	WasSession    bool
	WasRemembered bool
	// StillReadable reports that the read grant survives the removal because
	// the row also grants write, and write implies read: the honest answer for
	// "remove reading" on a writable root is that writing it means reading it.
	StillReadable bool
}

// Found reports whether the path held a read grant at all.
func (r ReadableRemoval) Found() bool { return r.WasSession || r.WasRemembered || r.StillReadable }

// ListReadablePaths returns every row that grants reading, remembered first
// (the table's own order), each with the label the dialog prints.
func ListReadablePaths() ([]ReadablePathEntry, []string) {
	s := grants.Process()
	if s == nil {
		return nil, nil
	}
	var out []ReadablePathEntry
	for _, g := range s.Entries().All() {
		if !g.Allow.Has(grants.Read) {
			continue
		}
		source := ReadableSourceSaved
		if g.Lifetime == grants.Session {
			source = ReadableSourceSession
		}
		out = append(out, ReadablePathEntry{Path: g.Path, Source: source, Detail: g.Source})
	}
	return out, nil
}

// AddReadablePath grants a directory for reading and persists it, so the next
// launch applies it without asking. The validation is the table's
// (grants.AddReadRoot refuses a root too broad to be a boundary), so a typo
// cannot turn into "read everything" whichever front-end made it.
func AddReadablePath(raw string) (path string, err error) {
	return grants.AddReadRoot(raw, grants.Remembered, "readable add")
}

// RemoveReadablePath revokes a read grant. A row that also grants write keeps
// reading — write implies read — and says so rather than pretending the
// revocation worked.
func RemoveReadablePath(raw string) (ReadableRemoval, error) {
	root, err := permissions.NormalizeReadRoot(raw)
	if err != nil {
		return ReadableRemoval{}, err
	}
	s := grants.Process()
	if s == nil {
		return ReadableRemoval{Path: root}, nil
	}
	if s.Allows(root, grants.Write) {
		return ReadableRemoval{Path: root, StillReadable: true}, nil
	}
	rem, err := s.RemoveCapability(root, grants.Read)
	if err != nil {
		return ReadableRemoval{Path: root}, err
	}
	return ReadableRemoval{Path: root, WasSession: rem.Session, WasRemembered: rem.Remembered}, nil
}

// ReadableSourceLabel renders the provenance of a readable path for the human,
// in the conversation's language.
func ReadableSourceLabel(source string, zh bool) string {
	switch {
	case source == ReadableSourceSaved && zh:
		return "（" + ReadableSourceSaved + "，已持久化）"
	case source == ReadableSourceSaved:
		return "(" + ReadableSourceSaved + ", remembered)"
	case zh:
		return "（仅本会话）"
	default:
		return "(this session)"
	}
}

// DisplayHomePath shortens a path under $HOME to "~/…" for display; the grant
// itself always uses the full path. It is ui's implementation, kept under this
// name because every caller in this package (and the TUI's pickers, which share
// the wording) reads it as cli's.
func DisplayHomePath(p string) string { return ui.DisplayHomePath(p) }
