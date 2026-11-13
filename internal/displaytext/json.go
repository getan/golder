// Package displaytext provides bounded, display-only text transformations shared
// by native and terminal views. It never changes stored tool messages.
package displaytext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
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

// PrettyJSONRead formats the JSON body of the read tool's numbered output.
// Each source line is prefixed with a line number and a tab; reformatting the
// body would otherwise fail json.Valid, which is why a one-line document used
// to reach both frontends unchanged. Line numbers and trailing read notices
// (pagination/truncation) are preserved; a document that is not valid JSON is
// returned exactly as it came in.
func PrettyJSONRead(text string) string {
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))
	first, width, end := 0, 0, 0
	for end < len(lines) {
		line := lines[end]
		tab := strings.IndexByte(line, '\t')
		if tab < 1 {
			break
		}
		prefix := line[:tab]
		number := strings.TrimSpace(prefix)
		if number == "" || strings.Trim(number, "0123456789") != "" {
			break
		}
		n, err := strconv.Atoi(number)
		if err != nil || n < 1 {
			break
		}
		if first == 0 {
			first, width = n, len(prefix)
		}
		body = append(body, line[tab+1:])
		end++
	}
	if first == 0 || len(body) == 0 {
		return text
	}
	raw := strings.Join(body, "\n")
	pretty := PrettyJSON(raw)
	if pretty == raw {
		return text
	}
	out := make([]string, 0, len(lines))
	for i, line := range strings.Split(pretty, "\n") {
		out = append(out, fmt.Sprintf("%*d\t%s", width, first+i, line))
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}
