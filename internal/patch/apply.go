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
	// Note, when non-empty, says how a hunk was located when it was not a
	// literal match (today: how many context lines were dropped). It is shown
	// with the summary so a reduced match is never silent.
	Note string
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
			updated, notes, err := applyChunks(lines, op.Chunks)
			if err != nil {
				return nil, applyErrf("Invalid Context: %v in %s", err, op.Path)
			}
			newContent := joinContent(updated, trailing)
			st.content = newContent
			if op.MoveTo == "" {
				// A hunk made entirely of context lines matches but changes
				// nothing. Reporting it as a change would write an identical file
				// and tell the caller "Applied 1 change(s)" with no diff — the
				// shape of a real edit that silently did nothing.
				if newContent == old {
					break
				}
				changes = append(changes, Change{Kind: ChangeUpdate, Path: full, OldContent: old, NewContent: newContent, Note: strings.Join(notes, "; ")})
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
			changes = append(changes, Change{Kind: ChangeMove, Path: dst, OldPath: full, OldContent: old, NewContent: newContent, Note: strings.Join(notes, "; ")})

		default:
			return nil, applyErrf("unknown patch operation %d", op.Kind)
		}
	}
	return changes, nil
}

// applyChunks walks the chunks in order, each searching from the end of the
// previous match so hunks apply top-to-bottom without rescanning the file.
func applyChunks(lines []string, chunks []Chunk) ([]string, []string, error) {
	var notes []string
	cursor := 0
	for i, ch := range chunks {
		// Anchors first: each named line moves the search forward, so a hunk
		// whose context repeats elsewhere lands under the function the patch
		// named instead of at the first copy in the file.
		start := cursor
		for _, anchor := range ch.Anchors {
			if at, ok := seekAnchor(lines, anchor, start); ok {
				start = at + 1
			}
		}
		old := chunkOldLines(ch.Lines)
		idx, ok := seekSequence(lines, old, start, ch.EOF)
		if !ok && start != cursor {
			// A stale anchor must not make a locatable hunk fail: retry from
			// the cursor, since the anchor is documentation, not a promise.
			idx, ok = seekSequence(lines, old, cursor, ch.EOF)
		}
		used := ch
		if !ok {
			// A hunk listed out of file order (or shifted by an earlier hunk)
			// still has a home: retry the whole file before failing. Sequential
			// application stays the normal path, so a patch that could apply to
			// an earlier duplicate does not silently move.
			if idx, ok = seekSequence(lines, old, 0, ch.EOF); !ok {
				// Last resort: drop context lines from the edges, as git apply
				// does, but only while the remainder is unique in the file — a
				// guess between two identical blocks is not an improvement.
				if shrunk, at, dropped, sok := shrinkChunk(lines, ch); sok {
					used, idx, ok = shrunk, at, true
					notes = append(notes, fmt.Sprintf("hunk %d applied with %d context line(s) reduced", i+1, dropped))
				}
			}
			if !ok {
				if ch.Header != "" {
					return nil, nil, fmt.Errorf("could not find the context for hunk %d (%s)%s", i+1, ch.Header, hunkDiagnostic(lines, old))
				}
				return nil, nil, fmt.Errorf("could not find the context for hunk %d%s", i+1, hunkDiagnostic(lines, old))
			}
		}
		old = chunkOldLines(used.Lines)
		// Context lines keep the FILE's text (they only anchored the match),
		// so a fuzzy hit never rewrites whitespace or punctuation; additions
		// come from the patch, deletions disappear.
		replacement := make([]string, 0, len(used.Lines))
		at := 0
		for _, ln := range used.Lines {
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
	return lines, notes, nil
}

// seekAnchor locates a "@@" hint. An exact (fuzzy-whitespace) line match is
// tried first; failing that, a line that CONTAINS the hint — models write
// "@@ func b()" for a line that reads "func b() {", and Codex's own examples
// do the same with "class BaseClass" — so containment is the useful meaning
// of an anchor. The hint only moves the search forward; it never fails a hunk.
func seekAnchor(lines []string, anchor string, start int) (int, bool) {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return 0, false
	}
	if at, ok := seekSequence(lines, []string{anchor}, start, false); ok {
		return at, true
	}
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		if strings.Contains(strings.TrimSpace(lines[i]), anchor) {
			return i, true
		}
	}
	return 0, false
}

// shrinkChunk drops context lines from the edges of a hunk until what remains
// matches exactly once in the file. It returns the trimmed chunk, the match
// position, and how many lines were dropped. Deletions and additions are never
// dropped — they are the edit — and a candidate that matches more than once is
// rejected: reducing context must not become guessing which copy was meant.
func shrinkChunk(lines []string, ch Chunk) (Chunk, int, int, bool) {
	first, last := -1, -1
	contexts := 0
	for i, ln := range ch.Lines {
		if ln.Kind == LineContext {
			contexts++
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 || contexts == 0 {
		return Chunk{}, 0, 0, false
	}
	for total := 1; total <= contexts; total++ {
		// Trailing context goes first: a prefix (a signature, a heading) usually
		// identifies the region better than the lines that follow it.
		for dropLead := 0; dropLead <= total; dropLead++ {
			dropTail := total - dropLead
			from, to := first-dropLead, last+dropTail
			if from < 0 || to >= len(ch.Lines) {
				continue
			}
			sub := ch
			sub.Anchors = nil
			sub.EOF = false
			sub.Lines = append([]Line(nil), ch.Lines[from:to+1]...)
			at, ok := uniqueMatch(lines, chunkOldLines(sub.Lines))
			if ok {
				return sub, at, total, true
			}
		}
	}
	return Chunk{}, 0, 0, false
}

// uniqueMatch finds the one position where pattern matches. It tries the exact
// comparison first and the normalized one second; a pattern that matches in
// more than one place at either strictness is ambiguous and refused.
func uniqueMatch(lines, pattern []string) (int, bool) {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return 0, false
	}
	for _, pass := range []matchPass{matchExact, matchNormalized} {
		count, at := 0, 0
		for i := 0; i+len(pattern) <= len(lines); i++ {
			if equalLines(lines[i:i+len(pattern)], pattern, pass) {
				count++
				at = i
				if count > 1 {
					break
				}
			}
		}
		if count == 1 {
			return at, true
		}
		if count > 1 {
			// Ambiguous at a stricter pass stays ambiguous at a looser one.
			return 0, false
		}
	}
	return 0, false
}

// hunkDiagnostic explains a failed match in the terms a patch author can act on:
// the closest line and the first expected/found pair that differs. A bare "could
// not find the context" leaves the author guessing whether the file moved, the
// hunk was stale, or the context was simply too short.
func hunkDiagnostic(lines, old []string) string {
	if len(old) == 0 || len(old) > len(lines) {
		return "; re-read the file and resend the hunk"
	}
	bestAt, bestHits := -1, 0
	for start := 0; start+len(old) <= len(lines); start++ {
		hits := 0
		for j := range old {
			if equalLine(lines[start+j], old[j], matchNormalized) {
				hits++
			}
		}
		if hits > bestHits {
			bestAt, bestHits = start, hits
		}
	}
	if bestAt < 0 || bestHits == 0 {
		return "; no line in the file matches the hunk's context; re-read the file and resend"
	}
	for j := range old {
		if equalLine(lines[bestAt+j], old[j], matchNormalized) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "; closest match at line %d (%d/%d context lines match; expected %q, found %q). The file there reads:",
			bestAt+1, bestHits, len(old), old[j], lines[bestAt+j])
		from := bestAt - 2
		if from < 0 {
			from = 0
		}
		to := bestAt + len(old) + 2
		if to > len(lines) {
			to = len(lines)
		}
		for k := from; k < to && k-from < 14; k++ {
			fmt.Fprintf(&b, "\n  %4d | %s", k+1, lines[k])
		}
		b.WriteString("\n  re-read the file and resend")
		return b.String()
	}
	return "; re-read the file and resend"
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
