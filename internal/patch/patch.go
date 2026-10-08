// Package patch implements the apply_patch DSL: a stripped-down, file-oriented
// diff format carried by one tool call, so a single invocation can add,
// update, move, and delete files. The grammar is codex's apply_patch:
//
//	*** Begin Patch
//	*** Add File: path            + one "+" line per content line
//	*** Delete File: path
//	*** Update File: path         optionally followed by "*** Move to: path"
//	@@ [context]                  then " ", "-", "+" lines; "*** End of File"
//	*** End Patch                 anchors a chunk to the end of the file
//
// This package parses and resolves a patch into the changes it would make; it
// never writes to disk. The caller decides how to persist the result, which
// keeps workspace-path policy, snapshots, and prompts in the tool layer where
// the rest of the agent already handles them. It imports only the standard
// library.
package patch

import (
	"fmt"
	"strings"
)

// LineKind is one line's role inside a chunk.
type LineKind int

const (
	// LineContext is unchanged text used to locate the hunk (" " prefix).
	LineContext LineKind = iota
	// LineAdd is inserted text ("+" prefix).
	LineAdd
	// LineDelete is removed text ("-" prefix).
	LineDelete
)

// Line is one chunk line.
type Line struct {
	Kind LineKind
	Text string
}

// Chunk is one hunk of an update: the context/removal lines locate the region
// (fuzzy-matched; see seek.go), and the additions replace it.
type Chunk struct {
	// Header is the text after "@@" when present (informational; it does not
	// constrain the match, matching codex's parser).
	Header string
	// Lines are the chunk's context, add, and delete lines in file order.
	Lines []Line
	// EOF anchors the search at the end of the file first (the
	// "*** End of File" marker).
	EOF bool
}

// OpKind is one file operation's kind.
type OpKind int

const (
	// OpAdd creates a new file (an existing path is an error).
	OpAdd OpKind = iota + 1
	// OpDelete removes an existing file.
	OpDelete
	// OpUpdate patches an existing file, optionally moving it (MoveTo).
	OpUpdate
)

// Op is one file operation in a patch.
type Op struct {
	Kind OpKind
	// Path is the target path exactly as written in the patch (the caller
	// resolves it against its workspace roots).
	Path string
	// MoveTo, for an update, is the rename target ("" = no rename). It is
	// resolved like Path.
	MoveTo string
	// Body holds the added lines for an Add (without the "+" prefix; each
	// entry is one line, and the created file ends with a newline).
	Body []string
	// Chunks holds the hunks for an Update.
	Chunks []Chunk
}

// ParseError is a malformed patch; it carries the 1-based input line so the
// model can see exactly which line broke the grammar.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("Invalid patch: line %d: %s", e.Line, e.Msg)
}

// Parse reads a full patch envelope into its operations. It requires the
// "*** Begin Patch" / "*** End Patch" markers and at least one file operation,
// so a truncated or empty tool call fails loudly instead of silently doing
// nothing.
func Parse(input string) ([]Op, error) {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	i := 0
	skipBlank := func() {
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	skipBlank()
	if i >= len(lines) || strings.TrimSpace(lines[i]) != beginMarker {
		return nil, &ParseError{Line: i + 1, Msg: "expected \"" + beginMarker + "\""}
	}
	i++

	var ops []Op
	for {
		skipBlank()
		if i >= len(lines) {
			return nil, &ParseError{Line: len(lines), Msg: "missing \"" + endMarker + "\""}
		}
		line := strings.TrimSpace(lines[i])
		switch {
		case line == endMarker:
			if len(ops) == 0 {
				return nil, &ParseError{Line: i + 1, Msg: "patch contains no file operations"}
			}
			return ops, nil
		case strings.HasPrefix(line, addPrefix):
			op, next, err := parseAdd(lines, i)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		case strings.HasPrefix(line, deletePrefix):
			path := strings.TrimSpace(strings.TrimPrefix(line, deletePrefix))
			if path == "" {
				return nil, &ParseError{Line: i + 1, Msg: "delete needs a file path"}
			}
			ops = append(ops, Op{Kind: OpDelete, Path: path})
			i++
		case strings.HasPrefix(line, updatePrefix):
			op, next, err := parseUpdate(lines, i)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		default:
			return nil, &ParseError{Line: i + 1, Msg: fmt.Sprintf("unexpected line %q (want %q, %q, %q or %q)",
				line, addPrefix+" path", deletePrefix+" path", updatePrefix+" path", endMarker)}
		}
	}
}

// parseAdd reads an Add File section: the header line plus every following
// "+" line, up to the next section header or the end marker.
func parseAdd(lines []string, i int) (Op, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), addPrefix))
	if path == "" {
		return Op{}, 0, &ParseError{Line: i + 1, Msg: "add needs a file path"}
	}
	op := Op{Kind: OpAdd, Path: path}
	i++
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == endMarker || isSectionHeader(trimmed) {
			break
		}
		if trimmed == "" {
			// Tolerate stray blank lines between entries.
			i++
			continue
		}
		if !strings.HasPrefix(lines[i], addLinePrefix) {
			return Op{}, 0, &ParseError{Line: i + 1, Msg: "add-file lines must start with \"+\""}
		}
		op.Body = append(op.Body, strings.TrimPrefix(lines[i], addLinePrefix))
		i++
	}
	if len(op.Body) == 0 {
		return Op{}, 0, &ParseError{Line: i, Msg: fmt.Sprintf("Add File %q has no content", path)}
	}
	return op, i, nil
}

// parseUpdate reads an Update File header, its optional Move-to line, and its
// "@@" chunks.
func parseUpdate(lines []string, i int) (Op, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), updatePrefix))
	if path == "" {
		return Op{}, 0, &ParseError{Line: i + 1, Msg: "update needs a file path"}
	}
	op := Op{Kind: OpUpdate, Path: path}
	i++

	if i < len(lines) {
		if move := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), movePrefix)); move != strings.TrimSpace(lines[i]) {
			if move == "" {
				return Op{}, 0, &ParseError{Line: i + 1, Msg: "move needs a destination path"}
			}
			op.MoveTo = move
			i++
		}
	}

	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == endMarker || isSectionHeader(trimmed) {
			break
		}
		if trimmed == "" {
			i++
			continue
		}
		if !strings.HasPrefix(trimmed, hunkPrefix) {
			return Op{}, 0, &ParseError{Line: i + 1, Msg: fmt.Sprintf("expected %q to start a hunk, got %q", hunkPrefix, trimmed)}
		}
		chunk := Chunk{Header: strings.TrimSpace(strings.TrimPrefix(trimmed, hunkPrefix))}
		i++
		for i < len(lines) {
			raw := lines[i]
			if strings.TrimSpace(raw) == "" {
				// A blank line inside a hunk is an empty context line: the
				// model may write it without the leading space.
				chunk.Lines = append(chunk.Lines, Line{Kind: LineContext})
				i++
				continue
			}
			if strings.TrimSpace(raw) == eofMarker {
				chunk.EOF = true
				i++
				break
			}
			if isSectionHeader(strings.TrimSpace(raw)) || strings.TrimSpace(raw) == endMarker {
				break
			}
			if strings.HasPrefix(strings.TrimRight(raw, " \t"), hunkPrefix) {
				// A new "@@" hunk inside the same Update File section: leave
				// it to the outer loop. Checked on the raw line so a context
				// line whose text begins with "@@" (" @@ ...") is not
				// mistaken for a boundary.
				break
			}
			kind, text, ok := splitChunkLine(raw)
			if !ok {
				return Op{}, 0, &ParseError{Line: i + 1, Msg: fmt.Sprintf(
					"unexpected line %q in hunk: every line must start with \" \" (context), %q (remove) or %q (add)",
					raw, delLinePrefix, addLinePrefix)}
			}
			chunk.Lines = append(chunk.Lines, Line{Kind: kind, Text: text})
			i++
		}
		if len(chunk.Lines) == 0 {
			return Op{}, 0, &ParseError{Line: i, Msg: "hunk has no lines"}
		}
		op.Chunks = append(op.Chunks, chunk)
	}
	if len(op.Chunks) == 0 && op.MoveTo == "" {
		return Op{}, 0, &ParseError{Line: i, Msg: fmt.Sprintf("Update File %q has no hunks", path)}
	}
	return op, i, nil
}

// splitChunkLine classifies one hunk line by its prefix. A line with no
// recognized prefix is invalid (the caller reports it).
func splitChunkLine(raw string) (LineKind, string, bool) {
	switch {
	case strings.HasPrefix(raw, addLinePrefix):
		return LineAdd, strings.TrimPrefix(raw, addLinePrefix), true
	case strings.HasPrefix(raw, delLinePrefix):
		return LineDelete, strings.TrimPrefix(raw, delLinePrefix), true
	case strings.HasPrefix(raw, contextLinePrefix):
		return LineContext, strings.TrimPrefix(raw, contextLinePrefix), true
	default:
		return 0, "", false
	}
}

// isSectionHeader reports whether a trimmed line starts a new file section.
func isSectionHeader(trimmed string) bool {
	return strings.HasPrefix(trimmed, addPrefix) ||
		strings.HasPrefix(trimmed, deletePrefix) ||
		strings.HasPrefix(trimmed, updatePrefix)
}

// Patch markers, matching codex's apply_patch grammar exactly so prompts and
// model habits carry over unchanged.
const (
	beginMarker       = "*** Begin Patch"
	endMarker         = "*** End Patch"
	addPrefix         = "*** Add File: "
	deletePrefix      = "*** Delete File: "
	updatePrefix      = "*** Update File: "
	movePrefix        = "*** Move to: "
	eofMarker         = "*** End of File"
	hunkPrefix        = "@@"
	addLinePrefix     = "+"
	delLinePrefix     = "-"
	contextLinePrefix = " "
)

// Paths returns every path the patch touches, in operation order, including a
// move's destination. It is the cheap extraction the security layers need
// (static denial, prompt summaries, compaction bookkeeping) before anything is
// resolved or written; an unparseable patch yields nil.
func Paths(input string) []string {
	ops, err := Parse(input)
	if err != nil {
		return nil
	}
	var paths []string
	for _, op := range ops {
		paths = append(paths, op.Path)
		if op.MoveTo != "" {
			paths = append(paths, op.MoveTo)
		}
	}
	return paths
}
