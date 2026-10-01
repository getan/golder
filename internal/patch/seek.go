package patch

import "strings"

// seekSequence finds the sequence of pattern lines within lines, beginning at
// or after start, returning the match's start index. Matching is attempted
// with decreasing strictness, ported from codex's apply_patch seek_sequence so
// a patch authored against slightly different whitespace (or typographic
// punctuation) still applies:
//
//  1. exact line equality;
//  2. ignoring trailing whitespace per line;
//  3. ignoring leading and trailing whitespace;
//  4. additionally normalizing common Unicode punctuation to ASCII (curly
//     quotes, en/em dashes, non-breaking spaces), the same leniency
//     `git apply` has for context matching.
//
// eof anchors the first attempt at the end of the file (the patch's
// "*** End of File" marker), falling back to the normal search if the anchor
// does not match. An empty pattern matches at start (a pure insertion).
func seekSequence(lines, pattern []string, start int, eof bool) (int, bool) {
	if len(pattern) == 0 {
		if start < 0 {
			start = 0
		}
		if start > len(lines) {
			start = len(lines)
		}
		return start, true
	}
	if len(pattern) > len(lines) {
		return 0, false
	}
	if start < 0 {
		start = 0
	}
	maxStart := len(lines) - len(pattern)
	if start > maxStart {
		return 0, false
	}

	if eof {
		// The anchor is a hint: try it first, then fall back to the normal
		// scan so an imperfect trailing context still resolves.
		at := len(lines) - len(pattern)
		if at >= start && equalLines(lines[at:at+len(pattern)], pattern, matchExact) {
			return at, true
		}
	}
	for pass := matchExact; pass <= matchNormalized; pass++ {
		for i := start; i <= maxStart; i++ {
			if equalLines(lines[i:i+len(pattern)], pattern, pass) {
				return i, true
			}
		}
	}
	return 0, false
}

// matchPass selects how strictly two lines are compared.
type matchPass int

const (
	matchExact matchPass = iota
	matchTrimRight
	matchTrimSpace
	matchNormalized
)

// equalLines compares two equal-length line sequences at the given strictness.
func equalLines(got, want []string, pass matchPass) bool {
	for i := range want {
		if !equalLine(got[i], want[i], pass) {
			return false
		}
	}
	return true
}

// equalLine compares one line under the pass's normalization.
func equalLine(got, want string, pass matchPass) bool {
	switch pass {
	case matchExact:
		return got == want
	case matchTrimRight:
		return strings.TrimRight(got, " \t") == strings.TrimRight(want, " \t")
	case matchTrimSpace:
		return strings.TrimSpace(got) == strings.TrimSpace(want)
	default:
		return normalizePunctuation(got) == normalizePunctuation(want)
	}
}

// punctuationNorm maps typographic punctuation to its ASCII equivalent so a
// patch typed with plain ASCII still matches source written with smart quotes
// or dashes.
var punctuationNorm = strings.NewReplacer(
	"\u2010", "-", // hyphen
	"\u2011", "-", // non-breaking hyphen
	"\u2012", "-", // figure dash
	"\u2013", "-", // en dash
	"\u2014", "-", // em dash
	"\u2015", "-", // horizontal bar
	"\u2212", "-", // minus sign
	"\u2018", "'", // left single quote
	"\u2019", "'", // right single quote
	"\u201a", "'", // single low quote
	"\u201c", "\"", // left double quote
	"\u201d", "\"", // right double quote
	"\u201e", "\"", // double low quote
	"\u00a0", " ", // non-breaking space
)

// normalizePunctuation lower-cases nothing (case is significant) and only
// rewrites punctuation, then trims surrounding whitespace.
func normalizePunctuation(s string) string {
	return strings.TrimSpace(punctuationNorm.Replace(s))
}
