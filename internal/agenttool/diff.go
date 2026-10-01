// This file holds the unified-diff renderer the write-side tools report in
// their result Details (the TUI turns Details["diff"] into a colored diff
// section). It was extracted from the former edit tool, which the apply_patch
// tool replaced; the algorithms are unchanged.
package agenttool

import (
	"fmt"
	"strings"
)

// diffContextLines is how many unchanged lines of context surround each change
// in the emitted hunks, matching git's default. Unchanged regions farther than
// this from any change are elided, so a one-line edit in a large file still
// produces a small diff.
const diffContextLines = 3

// unifiedDiff produces a unified diff between old and new content in the shape
// git emits: a ---/+++ header, then one "@@ -oldStart,oldCount +newStart,newCount @@"
// hunk per changed region with diffContextLines unchanged lines around it.
// Unchanged lines beyond the context are elided. It returns "" when the
// contents are equal.
func unifiedDiff(path, oldContent, newContent string) string {
	oldLines := splitLinesKeep(oldContent)
	newLines := splitLinesKeep(newContent)

	// Longest common subsequence over lines drives the -/+ markers.
	ops := numberDiffLines(diffLines(oldLines, newLines))
	hunks := groupHunks(ops, diffContextLines)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount)
		for _, op := range h.ops {
			switch op.kind {
			case diffEqual:
				fmt.Fprintf(&b, " %s\n", op.text)
			case diffDelete:
				fmt.Fprintf(&b, "-%s\n", op.text)
			case diffInsert:
				fmt.Fprintf(&b, "+%s\n", op.text)
			}
		}
	}
	return b.String()
}

// splitLinesKeep splits s into lines, dropping a single trailing newline so an
// empty final element is not produced for the common "ends with \n" case.
func splitLinesKeep(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffKind int

const (
	diffEqual diffKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffKind
	text string
	// oldNo / newNo are the 1-based line numbers the op occupies in the old /
	// new file (0 when it has no line on that side). An insert also records the
	// old-file line it precedes and a delete the new-file line it precedes -
	// the values a zero-count hunk header needs.
	oldNo, newNo int
}

// numberDiffLines stamps each op with its old/new line numbers by walking the
// ops with a counter per side: equal consumes both, delete only the old
// counter, insert only the new one.
func numberDiffLines(ops []diffOp) []diffOp {
	oldNo, newNo := 1, 1
	for i := range ops {
		switch ops[i].kind {
		case diffEqual:
			ops[i].oldNo, ops[i].newNo = oldNo, newNo
			oldNo++
			newNo++
		case diffDelete:
			ops[i].oldNo, ops[i].newNo = oldNo, newNo
			oldNo++
		case diffInsert:
			ops[i].oldNo, ops[i].newNo = oldNo, newNo
			newNo++
		}
	}
	return ops
}

// diffHunk is one hunk: a contiguous slice of ops plus the start and line
// counts its @@ header reports.
type diffHunk struct {
	ops                []diffOp
	oldStart, oldCount int
	newStart, newCount int
}

// groupHunks splits numbered ops into hunks: every op within ctx positions of
// a change belongs to that change's hunk, so nearby changes merge into one
// hunk while everything else is elided. It returns nil when nothing changed.
func groupHunks(ops []diffOp, ctx int) []diffHunk {
	n := len(ops)
	if n == 0 {
		return nil
	}

	// dist[i] is the distance in ops from i to the nearest change (0 for a
	// change itself), computed by one sweep from each side. far doubles as an
	// out-of-range index sentinel on both sides, so it must be far enough past
	// the array that the derived distance always exceeds ctx.
	far := n + ctx + 1
	dist := make([]int, n)
	for i := range dist {
		dist[i] = far
	}
	lastChange := -far
	for i := 0; i < n; i++ {
		if ops[i].kind != diffEqual {
			lastChange = i
		}
		if d := i - lastChange; d < dist[i] {
			dist[i] = d
		}
	}
	nextChange := far
	for i := n - 1; i >= 0; i-- {
		if ops[i].kind != diffEqual {
			nextChange = i
		}
		if d := nextChange - i; d < dist[i] {
			dist[i] = d
		}
	}

	var hunks []diffHunk
	for i := 0; i < n; {
		if dist[i] > ctx {
			i++
			continue
		}
		j := i
		for j < n && dist[j] <= ctx {
			j++
		}
		hunks = append(hunks, newHunk(ops[i:j]))
		i = j
	}
	return hunks
}

// newHunk computes a hunk's @@ header from its ops: the starts are the line
// numbers of the first old/new line in the hunk. A hunk with no line on one
// side is a pure insertion/deletion, and the header then reports the line
// *after which* it applies - one before the op's recorded number.
func newHunk(ops []diffOp) diffHunk {
	h := diffHunk{ops: ops}
	for _, op := range ops {
		switch op.kind {
		case diffEqual:
			h.oldCount++
			h.newCount++
			if h.oldStart == 0 {
				h.oldStart = op.oldNo
			}
			if h.newStart == 0 {
				h.newStart = op.newNo
			}
		case diffDelete:
			h.oldCount++
			if h.oldStart == 0 {
				h.oldStart = op.oldNo
			}
		case diffInsert:
			h.newCount++
			if h.newStart == 0 {
				h.newStart = op.newNo
			}
		}
	}
	if h.oldStart == 0 {
		h.oldStart = ops[0].oldNo - 1
	}
	if h.newStart == 0 {
		h.newStart = ops[0].newNo - 1
	}
	return h
}

// diffLines computes a line diff via a standard LCS dynamic-programming table,
// then backtracks to emit equal/delete/insert ops in order.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of LCS of a[i:] and b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{kind: diffEqual, text: a[i]})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			ops = append(ops, diffOp{kind: diffDelete, text: a[i]})
			i++
		} else {
			ops = append(ops, diffOp{kind: diffInsert, text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: diffDelete, text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: diffInsert, text: b[j]})
	}
	return ops
}
