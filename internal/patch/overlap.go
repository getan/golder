package patch

import "fmt"

// hunkLineOrigin follows source lines across insertions/deletions so an overlap
// names original line numbers, not the shifted positions of the working copy.
type hunkLineOrigin struct {
	line int // 1-based original line; zero for newly inserted text
	hunk int // the hunk that introduced replacement text; zero for untouched text
}

func overlappingHunks(first, second, line int) error {
	if line > 0 {
		return fmt.Errorf("hunks %d and %d both modify original line %d; merge these edits into one complete-line replacement hunk", first, second, line)
	}
	return fmt.Errorf("hunks %d and %d both modify newly inserted text; merge these edits into one hunk", first, second)
}

func currentHunkOverlap(ch Chunk, start, hunk int, origins []hunkLineOrigin) error {
	at := start
	for _, ln := range ch.Lines {
		switch ln.Kind {
		case LineDelete:
			if prior := origins[at]; prior.hunk != 0 {
				return overlappingHunks(prior.hunk, hunk, prior.line)
			}
			at++
		case LineContext:
			at++
		}
	}
	return nil
}

// A stale second hunk may no longer match the working copy at all. Only diagnose
// an overlap when its deleted text has an unambiguous home in the original;
// shared context alone is not a conflicting edit.
func staleHunkOverlap(ch Chunk, hunk int, original []string, edited map[int]int) error {
	if len(edited) == 0 {
		return nil
	}
	if at, ok := uniqueMatch(original, chunkOldLines(ch.Lines)); ok {
		for _, ln := range ch.Lines {
			if ln.Kind == LineDelete {
				if prior := edited[at+1]; prior != 0 {
					return overlappingHunks(prior, hunk, at+1)
				}
			}
			if ln.Kind != LineAdd {
				at++
			}
		}
	}
	for _, ln := range ch.Lines {
		if ln.Kind != LineDelete {
			continue
		}
		if at, ok := uniqueMatch(original, []string{ln.Text}); ok {
			if prior := edited[at+1]; prior != 0 {
				return overlappingHunks(prior, hunk, at+1)
			}
		}
	}
	return nil
}

// trackedReplacement preserves the file's actual context text on fuzzy hits.
// Each contiguous edit run inherits its first deleted original line; insert-only
// runs have no original line. All removed original lines retain their edit owner
// so a subsequent stale deletion can still be diagnosed after they disappear.
func trackedReplacement(lines []string, origins []hunkLineOrigin, start, hunk int, ch Chunk, edited map[int]int) ([]string, []hunkLineOrigin) {
	replacement := make([]string, 0, len(ch.Lines))
	provenance := make([]hunkLineOrigin, 0, len(ch.Lines))
	at := start
	for pos := 0; pos < len(ch.Lines); {
		if ch.Lines[pos].Kind == LineContext {
			replacement = append(replacement, lines[at])
			provenance = append(provenance, origins[at])
			at++
			pos++
			continue
		}
		end, scan, source := pos, at, 0
		for end < len(ch.Lines) && ch.Lines[end].Kind != LineContext {
			if ch.Lines[end].Kind == LineDelete {
				if source == 0 {
					source = origins[scan].line
				}
				scan++
			}
			end++
		}
		for _, ln := range ch.Lines[pos:end] {
			switch ln.Kind {
			case LineDelete:
				if sourceLine := origins[at].line; sourceLine != 0 {
					edited[sourceLine] = hunk
				}
				at++
			case LineAdd:
				replacement = append(replacement, ln.Text)
				provenance = append(provenance, hunkLineOrigin{line: source, hunk: hunk})
			}
		}
		pos = end
	}
	return replacement, provenance
}
