package mygoui

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/egoist/mygo/ui"
)

// Match the TUI's Chroma lexers and Catppuccin palettes. Tool cards are rebuilt
// during streaming, so keep a bounded cache without retaining entire results.
const (
	syntaxCacheEntries = 2048
	syntaxCacheBytes   = 4 << 20
	syntaxMaxLineBytes = 16 << 10
	syntaxLexerEntries = 1024
)

type syntaxCacheKey struct {
	lexer string
	text  string
	dark  bool
}

var syntaxCache = struct {
	sync.Mutex
	lines  map[syntaxCacheKey][]ui.Span
	lexers map[string]string
	bytes  int
}{lines: make(map[syntaxCacheKey][]ui.Span), lexers: make(map[string]string)}

func syntaxLexerName(path string) string {
	base := filepath.Base(path)
	syntaxCache.Lock()
	name, ok := syntaxCache.lexers[base]
	syntaxCache.Unlock()
	if ok {
		return name
	}
	if lexer := lexers.Match(base); lexer != nil {
		name = lexer.Config().Name
	}
	if len(base) <= 256 {
		syntaxCache.Lock()
		if len(syntaxCache.lexers) >= syntaxLexerEntries {
			clear(syntaxCache.lexers)
		}
		syntaxCache.lexers[strings.Clone(base)] = name
		syntaxCache.Unlock()
	}
	return name
}

// syntaxFileLine highlights one line, as the TUI does for diff rows. An
// unknown file, oversized line, or lexer that changes the source stays plain.
func syntaxFileLine(path, text string, dark bool) []ui.Span {
	plain := func() []ui.Span { return []ui.Span{{Text: text}} }
	if path == "" || len(text) > syntaxMaxLineBytes {
		return plain()
	}
	name := syntaxLexerName(path)
	if name == "" {
		return plain()
	}
	key := syntaxCacheKey{lexer: name, text: text, dark: dark}
	syntaxCache.Lock()
	if spans, ok := syntaxCache.lines[key]; ok {
		syntaxCache.Unlock()
		return spans
	}
	syntaxCache.Unlock()

	// Own the text before caching; it may otherwise retain a large tool output.
	key.text = strings.Clone(text)
	tokens, err := chroma.Tokenise(lexers.Get(name), nil, key.text)
	if err != nil || len(tokens) == 0 {
		return plain()
	}
	styleName := "catppuccin-latte"
	if dark {
		styleName = "catppuccin-mocha"
	}
	style := styles.Get(styleName)
	if style == nil {
		return plain()
	}
	spans := make([]ui.Span, 0, len(tokens))
	var rendered strings.Builder
	for _, token := range tokens {
		if token.Type == chroma.EOFType || token.Value == "" {
			continue
		}
		entry := style.Get(token.Type)
		span := ui.Span{Text: token.Value, Italic: entry.Italic == chroma.Yes}
		if entry.Bold == chroma.Yes {
			span.Weight = 700
		}
		if entry.Colour.IsSet() {
			span.Color = ui.RGB(entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue())
		}
		spans = append(spans, span)
		rendered.WriteString(token.Value)
	}
	// Some lexers append a newline. Remove only that synthetic suffix; never
	// alter code, indentation, or the text a user can select and copy.
	got := rendered.String()
	if got == text+"\n" && len(spans) > 0 {
		last := len(spans) - 1
		spans[last].Text = strings.TrimSuffix(spans[last].Text, "\n")
		if spans[last].Text == "" {
			spans = spans[:last]
		}
		got = strings.TrimSuffix(got, "\n")
	}
	if got != text || len(spans) == 0 {
		return plain()
	}
	cost := len(key.text) + len(key.lexer) + len(spans)*256
	if cost <= syntaxCacheBytes {
		syntaxCache.Lock()
		if cached, ok := syntaxCache.lines[key]; ok {
			syntaxCache.Unlock()
			return cached
		}
		if len(syntaxCache.lines) >= syntaxCacheEntries || syntaxCache.bytes+cost > syntaxCacheBytes {
			clear(syntaxCache.lines)
			syntaxCache.bytes = 0
		}
		syntaxCache.lines[key] = spans
		syntaxCache.bytes += cost
		syntaxCache.Unlock()
	}
	return spans
}

// numberedReadSpans preserves the exact read output, including line numbers,
// tabs, trailing newlines and truncation notices. Only numbered source lines
// are passed through the file's lexer.
func numberedReadSpans(path, output string, dark bool, gutter ui.Color) ([]ui.Span, bool) {
	if path == "" {
		return nil, false
	}
	var spans []ui.Span
	anyNumbered := false
	for _, line := range strings.SplitAfter(output, "\n") {
		if line == "" {
			continue
		}
		prefix, body, ok := strings.Cut(line, "\t")
		number := strings.TrimSpace(prefix)
		if !ok || number == "" || strings.Trim(number, "0123456789") != "" {
			spans = append(spans, ui.Span{Text: line})
			continue
		}
		anyNumbered = true
		spans = append(spans, ui.Span{Text: prefix + "\t", Color: gutter})
		spans = append(spans, syntaxFileLine(path, body, dark)...)
	}
	return spans, anyNumbered
}

func renderReadOutput(c *ui.Context, path, output string) bool {
	t := c.Theme()
	spans, ok := numberedReadSpans(path, output, t.Dark, t.TextMuted)
	return renderSyntaxOutput(c, spans, ok)
}

// grep results can contain several file types. Only ripgrep's unambiguous
// path:line:match form is interpreted; context, summaries and diagnostics
// remain verbatim instead of guessing where a hyphenated filename ends.
func grepResultSpans(output string, dark bool, gutter ui.Color) ([]ui.Span, bool) {
	var spans []ui.Span
	anyMatch := false
	for _, line := range strings.SplitAfter(output, "\n") {
		if line == "" {
			continue
		}
		path, prefix, body, ok := splitGrepMatch(line)
		if !ok {
			spans = append(spans, ui.Span{Text: line})
			continue
		}
		anyMatch = true
		spans = append(spans, ui.Span{Text: prefix, Color: gutter})
		spans = append(spans, syntaxFileLine(path, body, dark)...)
	}
	return spans, anyMatch
}

func splitGrepMatch(line string) (path, prefix, body string, ok bool) {
	for i := 1; i < len(line); i++ {
		if line[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j > i+1 && j < len(line) && line[j] == ':' {
			return line[:i], line[:j+1], line[j+1:], true
		}
	}
	return "", "", "", false
}

// Some patch results must retain their unified diff, e.g. deleting a file or
// a truncated preview. Keep every byte while highlighting changed code by the
// file named in the diff; unrelated output stays unchanged.
func patchResultSpans(output string, dark bool, add, remove ui.Color) ([]ui.Span, bool) {
	var spans []ui.Span
	path := ""
	oldHeader, inHunk, anyChange := false, false, false
	for _, line := range strings.SplitAfter(output, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "--- a/"):
			oldHeader, inHunk = true, false
		case oldHeader && strings.HasPrefix(line, "+++ b/"):
			path = strings.TrimSuffix(strings.TrimPrefix(line, "+++ b/"), "\n")
			oldHeader = false
		case path != "" && strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && (line[0] == '+' || line[0] == '-'):
			color := add
			if line[0] == '-' {
				color = remove
			}
			spans = append(spans, ui.Span{Text: line[:1], Color: color})
			spans = append(spans, syntaxFileLine(path, line[1:], dark)...)
			anyChange = true
			continue
		case inHunk && line[0] != ' ' && line[0] != '\\':
			inHunk = false
		}
		spans = append(spans, ui.Span{Text: line})
	}
	return spans, anyChange
}

func renderGrepOutput(c *ui.Context, output string) bool {
	t := c.Theme()
	spans, ok := grepResultSpans(output, t.Dark, t.TextMuted)
	return renderSyntaxOutput(c, spans, ok)
}

func renderPatchResultOutput(c *ui.Context, output string) bool {
	t := c.Theme()
	spans, ok := patchResultSpans(output, t.Dark, t.Success, t.Danger)
	return renderSyntaxOutput(c, spans, ok)
}

func renderSyntaxOutput(c *ui.Context, spans []ui.Span, ok bool) bool {
	if !ok {
		return false
	}
	ui.RichText(c, spans...).MarginStart(18).Font("monospace").FontSize(12).TextColor(c.Theme().Text).Selectable().Wrap()
	return true
}
