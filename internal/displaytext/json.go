// Package displaytext provides bounded, display-only text transformations shared
// by native and terminal views. It never changes stored tool messages.
package displaytext

import (
	"bytes"
	"encoding/json"
	"strings"
)

// PrettyJSON preserves numeric spelling/key order and leaves non-JSON alone.
// Reject deep or expansion-heavy inputs before json.Indent allocates output.
func PrettyJSON(text string) string {
	if len(text) > 512<<10 {
		return text
	}
	raw := strings.TrimSpace(text)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') || !json.Valid([]byte(raw)) {
		return text
	}
	depth, budget := 0, len(raw)
	quoted, escape := false, false
	for _, c := range raw {
		if quoted {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			budget += depth*2 + 2
		case '}', ']':
			budget += depth*2 + 2
			depth--
		case ',':
			budget += depth*2 + 2
		}
		if depth > 64 || budget > 2<<20 {
			return text
		}
	}
	var out bytes.Buffer
	if json.Indent(&out, []byte(raw), "", "  ") != nil {
		return text
	}
	return out.String()
}
