package tui

import (
	"strings"
	"unicode/utf8"
)

// A single transcript block can be arbitrarily large: a build diff with one
// minified line, a 100 MB tool result, a pasted log. Glamour, lipgloss and the
// diff highlighter all walk every rune, and the transcript's per-block budget
// cannot interrupt one expensive block. Clip the source before it reaches any
// renderer: long lines get a marker, and the whole block gets a bounded
// preview. The session still holds the full text; this only bounds what a
// frame draws.
const (
	renderMaxLineBytes = 4 << 10
	renderMaxTextBytes = 64 << 10
	renderTruncated    = "… (display truncated; the full content is in the session)"
)

// clipRenderText bounds every line and the whole text. It is cheap on text
// that is already small: one scan for an over-long line, none otherwise.
func clipRenderText(s string) string {
	if len(s) <= renderMaxTextBytes && !hasLongRenderLine(s) {
		return s
	}
	if len(s) > renderMaxTextBytes {
		s = truncateUTF8(s, renderMaxTextBytes)
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += renderTruncated
	}
	var b strings.Builder
	b.Grow(len(s))
	start := 0
	for i := 0; i <= len(s); i++ {
		if i != len(s) && s[i] != '\n' {
			continue
		}
		line := s[start:i]
		if len(line) > renderMaxLineBytes {
			line = truncateUTF8(line, renderMaxLineBytes) + " …"
		}
		b.WriteString(line)
		if i < len(s) {
			b.WriteByte('\n')
		}
		start = i + 1
	}
	return b.String()
}

// clipRenderLine bounds one line without the whole-block notice, for rows a
// caller is already drawing one at a time.
func clipRenderLine(s string) string {
	if len(s) <= renderMaxLineBytes {
		return s
	}
	return truncateUTF8(s, renderMaxLineBytes) + " …"
}

func hasLongRenderLine(s string) bool {
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			start = i + 1
			continue
		}
		if i-start >= renderMaxLineBytes {
			return true
		}
	}
	return false
}

// truncateUTF8 cuts at n bytes without leaving half a rune at the end.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
