package agenttool

import "strings"

// Reject recognizable non-CSS locator dialects without interpreting quoted
// attribute values or escaped CSS characters as selector syntax.
func unsupportedBrowserSelector(selector string) bool {
	s := strings.TrimSpace(selector)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "text=") || strings.HasPrefix(lower, "nth=") || strings.HasPrefix(lower, "/") && !strings.HasPrefix(lower, "/*") {
		return true
	}
	var quote byte
	escaped := false
	for i := 0; i < len(lower); i++ {
		ch := lower[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if strings.HasPrefix(lower[i:], ":has-text(") || strings.HasPrefix(lower[i:], ":text(") || strings.HasPrefix(lower[i:], ">>") {
			return true
		}
	}
	return false
}
