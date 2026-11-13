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
// body would otherwise fail json.Valid. Large minified documents are often cut
// by the read tool's per-line budget, so an incomplete prefix is still laid out
// structurally and the original truncation marker is retained. Line numbers and
// trailing read notices are preserved; a document that is not JSON-like is
// returned exactly as it came in.
func PrettyJSONRead(text string) string {
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))
	first, width, end := 0, 0, 0
	cut := ""
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
		content := line[tab+1:]
		if marker := strings.Index(content, "… (line truncated:"); marker >= 0 {
			body = append(body, content[:marker])
			cut = content[marker:]
			end++
			break
		}
		body = append(body, content)
		end++
	}
	if first == 0 || len(body) == 0 {
		return text
	}
	raw := strings.Join(body, "\n")
	pretty := ""
	if json.Valid([]byte(raw)) {
		pretty = PrettyJSON(raw)
		if pretty == raw {
			return text
		}
	} else if cut != "" || strings.Contains(text, "... (output truncated") || strings.Contains(text, "... (read stopped before line") {
		pretty = prettyJSONPrefix(raw)
		if pretty == "" {
			return text
		}
	} else {
		return text
	}
	out := make([]string, 0, len(lines)+strings.Count(pretty, "\n"))
	for i, line := range strings.Split(pretty, "\n") {
		out = append(out, fmt.Sprintf("%*d\t%s", width, first+i, line))
	}
	if cut != "" {
		out = append(out, cut)
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// prettyJSONPrefix lays out a JSON prefix that is not valid JSON yet, either
// because the read tool cut a long line or because the output budget stopped in
// the middle of a document. It never invents closing delimiters or values; the
// original prefix remains visible and the caller keeps the truncation marker.
func prettyJSONPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512<<10 || (raw[0] != '{' && raw[0] != '[') {
		return ""
	}
	out := make([]byte, 0, len(raw)+len(raw)/3)
	indent := 0
	inString, escape := false, false
	newline := func() {
		out = append(out, '\n')
		for range indent {
			out = append(out, ' ', ' ')
		}
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '{', '[':
			out = append(out, c)
			indent++
			newline()
		case '}', ']':
			if indent > 0 {
				indent--
			}
			out = trimJSONSpace(out)
			newline()
			out = append(out, c)
		case ',':
			out = append(out, c)
			newline()
		case ':':
			out = append(out, ':', ' ')
		case ' ', '\t', '\r', '\n':
			// Whitespace outside strings carries no meaning here.
		default:
			out = append(out, c)
		}
	}
	if inString {
		return string(out)
	}
	return string(trimJSONSpace(out))
}

func trimJSONSpace(b []byte) []byte {
	for len(b) > 0 {
		switch b[len(b)-1] {
		case ' ', '\t', '\r', '\n':
			b = b[:len(b)-1]
		default:
			return b
		}
	}
	return b
}
