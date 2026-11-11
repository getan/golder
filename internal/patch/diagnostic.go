package patch

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	diagnosticLineBytes = 400
	diagnosticMaxBytes  = 4096
)

// diagnosticPreview bounds the escaped representation as well as the source
// prefix: quotes, control bytes and backslashes must not expand a huge line
// into an even larger failure. Previews are explicitly not patch context.
func diagnosticPreview(text string) string {
	n := min(len(text), diagnosticLineBytes)
	for {
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		quoted := strconv.Quote(text[:n])
		if len(quoted) <= diagnosticLineBytes {
			if n < len(text) {
				return fmt.Sprintf("%s… (%d bytes total; preview only)", quoted, len(text))
			}
			return quoted
		}
		n = n * 3 / 4
	}
}

// inlineContextLine only explains a failure; it never relaxes matching. Prefer
// the longest substantial fragment so punctuation or a shared JSX tag does not
// hide the actual long source line. Also recognize an old read truncation marker.
func inlineContextLine(lines, old []string) (int, bool) {
	bestAt, best := -1, 7
	for _, expected := range old {
		expected, _, _ = strings.Cut(expected, "… (line truncated")
		expected = strings.TrimSpace(expected)
		if len(expected) <= best {
			continue
		}
		for i, line := range lines {
			if len(line) > len(expected) && !equalLine(line, expected, matchWhitespace) && strings.Contains(line, expected) {
				bestAt, best = i, len(expected)
				break
			}
		}
	}
	return bestAt, bestAt >= 0
}

func completeLineAdvice(line, size int) string {
	if size <= diagnosticLineBytes {
		return fmt.Sprintf("The source line is short enough to re-read in full: read offset=%d, limit=1, then rebuild the hunk from that complete line", line)
	}
	return fmt.Sprintf("Line %d is one %d-byte source line. Obtain the complete line with read offset=%d, limit=1 (raise max_line_bytes within the reader's maximum if needed), or split/format the source first. Truncated previews are not valid patch context", line, size, line)
}

func inlineFragmentDiagnostic(lines, old []string) string {
	if idx, ok := inlineContextLine(lines, old); ok {
		return fmt.Sprintf("; the closest line is %d (%d bytes): %s. Hunk context is an inline fragment; apply_patch matches whole lines, not substrings. Use one complete -/+ line replacement; do not split a single source line across context rows. %s",
			idx+1, len(lines[idx]), diagnosticPreview(lines[idx]), completeLineAdvice(idx+1, len(lines[idx])))
	}
	return ""
}
