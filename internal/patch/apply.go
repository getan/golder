package patch

import (
	"fmt"
	"os"
	"strings"
)

// Resolver maps a patch path (exactly as written in the patch) to the absolute
// path it targets. It is the caller's workspace-boundary policy: the engine
// never interprets paths itself, so root confinement stays in one place.
type Resolver func(path string) (string, error)

// ChangeKind is what an operation does to one file.
type ChangeKind int

const (
	// ChangeAdd creates a file.
	ChangeAdd ChangeKind = iota + 1
	// ChangeUpdate rewrites a file in place.
	ChangeUpdate
	// ChangeDelete removes a file.
	ChangeDelete
	// ChangeMove rewrites a file and renames it.
	ChangeMove
)

// Change is one resolved, in-memory file change. Nothing has been written when
// Compute returns; the caller persists each change (and can snapshot it
// first).
type Change struct {
	Kind ChangeKind
	// Path is the resolved destination path (for Delete and Update, the file
	// itself; for Move, the new location).
	Path string
	// OldPath is the resolved source path for a Move ("" otherwise).
	OldPath string
	// OldContent is the file content before the change ("" for Add).
	OldContent string
	// NewContent is the content after the change ("" for Delete).
	NewContent string
}

// ApplyError reports a patch that parsed but cannot be applied; the message is
// written for the model (it names the operation and path), mirroring codex's
// error vocabulary so existing model habits hold.
type ApplyError struct {
	Msg string
}

func (e *ApplyError) Error() string { return e.Msg }

func applyErrf(format string, args ...any) error {
	return &ApplyError{Msg: fmt.Sprintf(format, args...)}
}

// Compute resolves a parsed patch against the working tree and returns the
// changes it would make, in operation order, without touching the filesystem.
// Existence checks and content reads happen here, so every failure (missing
// file for an update, an existing file for an add, a poor context match)
// surfaces before the first byte is written. Operations are applied over an
// in-memory overlay, so a patch that touches the same file twice sees its own
// earlier change.
func Compute(ops []Op, resolve Resolver) ([]Change, error) {
	// overlay caches each resolved path's current working-tree content, so
	// repeated operations on one file compose instead of racing the disk.
	type fileState struct {
		content string
		exists  bool
		isDir   bool
	}
	overlay := map[string]*fileState{}
	state := func(full string) (*fileState, error) {
		if st, ok := overlay[full]; ok {
			return st, nil
		}
		st := &fileState{}
		info, err := os.Stat(full)
		switch {
		case err == nil && info.IsDir():
			st.exists, st.isDir = true, true
		case err == nil:
			data, readErr := os.ReadFile(full)
			if readErr != nil {
				return nil, applyErrf("cannot read %s: %v", full, readErr)
			}
			st.exists, st.content = true, string(data)
		case os.IsNotExist(err):
			// absent: the zero value is the correct state
		default:
			return nil, applyErrf("cannot stat %s: %v", full, err)
		}
		overlay[full] = st
		return st, nil
	}

	var changes []Change
	for _, op := range ops {
		full, err := resolve(op.Path)
		if err != nil {
			return nil, err
		}
		st, err := state(full)
		if err != nil {
			return nil, err
		}

		switch op.Kind {
		case OpAdd:
			if st.exists {
				if st.isDir {
					return nil, applyErrf("Add File Error: %s is a directory", op.Path)
				}
				return nil, applyErrf("Add File Error: file already exists: %s", op.Path)
			}
			content := joinContent(op.Body, true)
			st.exists, st.content = true, content
			changes = append(changes, Change{Kind: ChangeAdd, Path: full, NewContent: content})

		case OpDelete:
			if !st.exists {
				return nil, applyErrf("Delete File Error: file does not exist: %s", op.Path)
			}
			if st.isDir {
				return nil, applyErrf("Delete File Error: %s is a directory", op.Path)
			}
			old := st.content
			st.exists, st.content = false, ""
			changes = append(changes, Change{Kind: ChangeDelete, Path: full, OldContent: old})

		case OpUpdate:
			if !st.exists {
				return nil, applyErrf("Update File Error: file does not exist: %s", op.Path)
			}
			if st.isDir {
				return nil, applyErrf("Update File Error: %s is a directory", op.Path)
			}
			old := st.content
			lines, trailing := splitContent(old)
			updated, err := applyChunks(lines, op.Chunks)
			if err != nil {
				return nil, applyErrf("Invalid Context: %v in %s", err, op.Path)
			}
			newContent := joinContent(updated, trailing)
			st.content = newContent
			if op.MoveTo == "" {
				changes = append(changes, Change{Kind: ChangeUpdate, Path: full, OldContent: old, NewContent: newContent})
				break
			}
			dst, err := resolve(op.MoveTo)
			if err != nil {
				return nil, err
			}
			dstState, err := state(dst)
			if err != nil {
				return nil, err
			}
			if dstState.exists {
				return nil, applyErrf("Move Error: destination already exists: %s", op.MoveTo)
			}
			if dst == full {
				return nil, applyErrf("Move Error: destination equals source: %s", op.MoveTo)
			}
			st.exists = false
			dstState.exists, dstState.content = true, newContent
			changes = append(changes, Change{Kind: ChangeMove, Path: dst, OldPath: full, OldContent: old, NewContent: newContent})

		default:
			return nil, applyErrf("unknown patch operation %d", op.Kind)
		}
	}
	return changes, nil
}

// applyChunks walks the chunks in order, each searching from the end of the
// previous match so hunks apply top-to-bottom without rescanning the file.
func applyChunks(lines []string, chunks []Chunk) ([]string, error) {
	cursor := 0
	for i, ch := range chunks {
		old := chunkOldLines(ch.Lines)
		idx, ok := seekSequence(lines, old, cursor, ch.EOF)
		if !ok {
			if ch.Header != "" {
				return nil, fmt.Errorf("could not find the context for hunk %d (%s)", i+1, ch.Header)
			}
			return nil, fmt.Errorf("could not find the context for hunk %d", i+1)
		}
		// Context lines keep the FILE's text (they only anchored the match),
		// so a fuzzy hit never rewrites whitespace or punctuation; additions
		// come from the patch, deletions disappear.
		replacement := make([]string, 0, len(ch.Lines))
		at := 0
		for _, ln := range ch.Lines {
			switch ln.Kind {
			case LineContext:
				replacement = append(replacement, lines[idx+at])
				at++
			case LineDelete:
				at++
			case LineAdd:
				replacement = append(replacement, ln.Text)
			}
		}
		merged := make([]string, 0, len(lines)-len(old)+len(replacement))
		merged = append(merged, lines[:idx]...)
		merged = append(merged, replacement...)
		merged = append(merged, lines[idx+len(old):]...)
		lines = merged
		cursor = idx + len(replacement)
	}
	return lines, nil
}

// chunkOldLines returns the lines the matcher must locate: the chunk's context
// and deletion lines, in order.
func chunkOldLines(lines []Line) []string {
	var old []string
	for _, ln := range lines {
		switch ln.Kind {
		case LineContext:
			old = append(old, ln.Text)
		case LineDelete:
			old = append(old, ln.Text)
		}
	}
	return old
}

// splitContent splits content into lines plus whether it ended with a
// newline, so an edit preserves the file's trailing-newline convention.
func splitContent(s string) (lines []string, trailingNewline bool) {
	if s == "" {
		return nil, false
	}
	lines = strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1], true
	}
	return lines, false
}

// joinContent is splitContent's inverse.
func joinContent(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		return ""
	}
	s := strings.Join(lines, "\n")
	if trailingNewline {
		s += "\n"
	}
	return s
}
