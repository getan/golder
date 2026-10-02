package tui

// Shell-command syntax highlighting for tool-card headlines. Codex renders
// each `Ran` command through syntect with the Catppuccin palette (mocha on
// dark terminals, latte on light); pigo gets the same result from chroma,
// which is already in the dependency graph (glamour renders fenced code with
// it) and ships both Catppuccin themes. The lexer is bash for every shell
// command: pigo's bash tool runs a POSIX shell on unix and Git Bash/WSL on
// Windows, and the bash lexer tokenizes the common subset those all accept.

import (
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// hlSpan is one syntax-colored run of a command line. An empty color means
// "terminal default for command text" (the caller's fallback style), which is
// what chroma reports for token types the active theme leaves unstyled.
// literal marks string/char tokens (opaque to the command-word pass: a `|`
// inside a quoted pattern is data, not a pipeline). plain marks tokens the
// theme renders as ordinary text, which is the only class the command-word
// pass may recolor.
type hlSpan struct {
	text    string
	color   string // "#rrggbb"
	bold    bool
	italic  bool
	plain   bool
	literal bool
}

const cmdHLCacheMax = 256

var (
	cmdHLMu    sync.Mutex
	cmdHLCache = map[string][]hlSpan{}
)

// syntaxDark reports whether Markdown (and therefore command) syntax coloring
// should use the dark palette. It reads the same flag SetMarkdownDark records
// from the terminal's background color.
func syntaxDark() bool {
	mdMu.Lock()
	defer mdMu.Unlock()
	return mdDark
}

// highlightShellCommand tokenizes cmd and maps chroma's tokens onto the
// Catppuccin palette codex uses. Results are cached per (command, palette):
// a card re-renders on every reflow, and a streaming turn reflows often.
// A single plain span is returned when the lexer or theme is unavailable, so
// the command always renders.
func highlightShellCommand(cmd string, dark bool) []hlSpan {
	// The bash lexer leaves external command names as plain text; the
	// positional pass paints them the way codex's syntax theme does.
	return recolorCommandWords(highlightCached("bash", cmd, dark), commandWordColor(dark), dark)
}

// highlightCodeLine tokenizes one line of file content with the lexer for
// path's file type, using the same Catppuccin palette as command highlighting
// (and as the Markdown code blocks). Line-at-a-time tokenization cannot see
// multi-line state (a block comment, a raw string), which can miscolor a few
// tokens; diff rows are short, and this keeps a reflow from running a
// stateful lexer over the whole file. Unknown file types fall back to a
// single uncolored span, so the line still renders.
func highlightCodeLine(path, line string, dark bool) []hlSpan {
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		return []hlSpan{{text: line}}
	}
	return highlightCached(lexer.Config().Name, line, dark)
}

// highlightCached is the shared cache/fallback path for the two public
// highlighters: results are memoized per (lexer, text, palette) because tool
// cards re-render on every reflow, and a streaming turn reflows often.
func highlightCached(lexerName, text string, dark bool) []hlSpan {
	key := "light\x00" + lexerName + "\x00"
	if dark {
		key = "dark\x00" + lexerName + "\x00"
	}
	key += text
	cmdHLMu.Lock()
	if spans, ok := cmdHLCache[key]; ok {
		cmdHLMu.Unlock()
		return spans
	}
	cmdHLMu.Unlock()

	spans := tokenise(lexers.Get(lexerName), text, dark)

	cmdHLMu.Lock()
	if len(cmdHLCache) >= cmdHLCacheMax {
		cmdHLCache = map[string][]hlSpan{}
	}
	cmdHLCache[key] = spans
	cmdHLMu.Unlock()
	return spans
}

// tokenise runs one lexer over text and resolves each token's color from the
// Catppuccin theme. Adjacent tokens sharing a style are merged so a wrapped
// line renders as few SGR runs as possible. A nil lexer yields one plain span.
func tokenise(lexer chroma.Lexer, text string, dark bool) []hlSpan {
	if lexer == nil {
		return []hlSpan{{text: text}}
	}
	tokens, err := chroma.Tokenise(lexer, nil, text)
	if err != nil || len(tokens) == 0 {
		return []hlSpan{{text: text}}
	}
	name := "catppuccin-mocha"
	if !dark {
		name = "catppuccin-latte"
	}
	style := styles.Get(name)
	if style == nil {
		style = styles.Fallback
	}
	var spans []hlSpan
	for _, tok := range tokens {
		if tok.Value == "" || tok.Type == chroma.EOFType {
			continue
		}
		entry := style.Get(tok.Type)
		next := hlSpan{
			text:    tok.Value,
			bold:    entry.Bold == chroma.Yes,
			italic:  entry.Italic == chroma.Yes,
			plain:   tok.Type == chroma.Text,
			literal: tok.Type.InCategory(chroma.Literal),
		}
		if entry.Colour.IsSet() {
			next.color = entry.Colour.String()
		}
		spans = appendHLSpan(spans, next)
	}
	if len(spans) == 0 {
		return []hlSpan{{text: text}}
	}
	return spans
}

func sameHLStyle(a, b hlSpan) bool {
	return a.color == b.color && a.bold == b.bold && a.italic == b.italic &&
		a.plain == b.plain && a.literal == b.literal
}

// appendHLSpan appends s, merging it into the previous span when the style is
// identical (fewer SGR runs per line).
func appendHLSpan(spans []hlSpan, s hlSpan) []hlSpan {
	if s.text == "" {
		return spans
	}
	if n := len(spans); n > 0 && sameHLStyle(spans[n-1], s) {
		spans[n-1].text += s.text
		return spans
	}
	return append(spans, s)
}

// commandWordColor is the palette slot for a shell command name: codex's
// syntax theme (Catppuccin) colors function/command scopes blue
// (NameFunction #89b4fa mocha, #1e66f5 latte).
func commandWordColor(dark bool) string {
	if dark {
		return "#89b4fa"
	}
	return "#1e66f5"
}

// optionColor is the palette slot for command options: Catppuccin gives
// variable.parameter mauve-ish maroon (mocha #eba0ac, latte #e64553) with an
// italic style, which is what codex renders for `-n`, `--flag`, `-15`.
func optionColor(dark bool) string {
	if dark {
		return "#eba0ac"
	}
	return "#e64553"
}

// isOptionToken reports whether a whitespace-delimited shell word is an option:
// `-n`, `--no-preserve-root`, `-15`, or a bundled short form like `-la`.
// Bare punctuation (`-` alone, `--`) and negative-looking non-flags are left
// alone.
func isOptionToken(word string) bool {
	if len(word) < 2 || word[0] != '-' {
		return false
	}
	if word[1] == '-' {
		// `--long`, but not a bare `--` separator.
		return len(word) > 2 && isWordRune(rune(word[2]))
	}
	// `-x`, `-15`, `-la`: the character after the dash must look like a flag
	// body (letter or digit), never another dash or an operator.
	return isWordRune(rune(word[1]))
}

// isWordRune reports whether r can appear in a shell option body.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// recolorCommandWords walks the token spans and paints the first word of each
// command position blue, matching codex's rendering of `grep`/`head`/`sed`.
// chroma's bash lexer leaves external commands as plain text (only builtins
// like echo get a scope), so parity needs this small positional pass:
//
//   - a word after `;`, `|`, `&`, `(` or at the start begins a command,
//   - `NAME=value` prefixes keep the command position open (`M=$(…) cmd`),
//   - quoted strings are opaque (a `|` inside a grep pattern is data),
//   - only genuinely plain tokens are recolored, so builtins, variables,
//     keywords and operators keep their theme colors.
func recolorCommandWords(spans []hlSpan, color string, dark bool) []hlSpan {
	out := make([]hlSpan, 0, len(spans))
	expect := true
	for _, s := range spans {
		if s.literal {
			out = appendHLSpan(out, s)
			expect = false
			continue
		}
		text := s.text
		for i := 0; i < len(text); {
			r, size := utf8.DecodeRuneInString(text[i:])
			switch {
			case r == ' ' || r == '\t' || r == '\n':
				out = appendHLSpan(out, withText(s, string(r)))
				i += size
			case isShellOperator(r):
				if r != ')' {
					expect = true
				}
				out = appendHLSpan(out, withText(s, string(r)))
				i += size
			default:
				j := i + size
				for j < len(text) {
					rr, sz := utf8.DecodeRuneInString(text[j:])
					if rr == ' ' || rr == '\t' || rr == '\n' || isShellOperator(rr) {
						break
					}
					j += sz
				}
				word := text[i:j]
				switch {
				case isOptionToken(word):
					// chroma leaves options as plain text; Sublime scopes them
					// variable.parameter.option.shell and Catppuccin paints
					// that maroon italic — the color codex shows for `-n`,
					// `--no-preserve-root`, `-15`. Options never end the
					// command position (`grep -n foo` still paints grep, and
					// `FOO=1 cmd -x` keeps scanning).
					styled := withText(s, word)
					if s.plain {
						styled.color = optionColor(dark)
						styled.italic = true
					}
					out = appendHLSpan(out, styled)
				case !expect:
					out = appendHLSpan(out, withText(s, word))
				case isAssignmentWord(word):
					// A NAME=value prefix is not the command; stay in the
					// command position so `FOO=1 grep …` paints grep.
					out = appendHLSpan(out, withText(s, word))
				case s.plain && startsLikeCommand(word):
					recolored := withText(s, word)
					recolored.color = color
					recolored.bold, recolored.italic = false, false
					out = appendHLSpan(out, recolored)
					expect = false
				default:
					out = appendHLSpan(out, withText(s, word))
					expect = false
				}
				i = j
			}
		}
	}
	return out
}

// withText returns a copy of s carrying text.
func withText(s hlSpan, text string) hlSpan {
	s.text = text
	return s
}

// isShellOperator reports whether r separates commands or opens a subshell.
func isShellOperator(r rune) bool {
	switch r {
	case ';', '|', '&', '(', ')':
		return true
	default:
		return false
	}
}

// isAssignmentWord reports whether word is a NAME=value shell assignment.
func isAssignmentWord(word string) bool {
	if strings.HasPrefix(word, "-") || strings.HasPrefix(word, "$") {
		return false
	}
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range word[:eq] {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// startsLikeCommand reports whether word can be a command name (so `.` `/`
// `~` scripts match, while flags, variables and quoted text do not).
func startsLikeCommand(word string) bool {
	if word == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(word)
	if r == '-' || r == '$' || r == '"' || r == '\'' || r == '#' {
		return false
	}
	return unicode.IsLetter(r) || r == '_' || r == '.' || r == '/' || r == '~'
}

// wrapHLSpans wraps styled spans to first display columns on the first line and
// cont on every following line, with codex's wrap semantics (textwrap
// WordSplitter::NoHyphenation): lines break at spaces, and only a single word
// wider than the line is hard-broken at rune boundaries (CJK-safe). Hard
// newlines force a line. A returned line may be empty (a leading newline, or a
// command that is all whitespace).
func wrapHLSpans(spans []hlSpan, first, cont int) [][]hlSpan {
	if cont < 1 {
		cont = 1
	}
	if first < 1 {
		first = 1
	}
	type srun struct {
		r rune
		s hlSpan
	}
	var flat []srun
	for _, s := range spans {
		for _, r := range s.text {
			flat = append(flat, srun{r: r, s: s})
		}
	}

	var lines [][]hlSpan
	cur := []hlSpan{}
	width, limit := 0, first
	flush := func() {
		lines = append(lines, cur)
		cur = []hlSpan{}
		width, limit = 0, cont
	}
	appendRune := func(sr srun) {
		cur = appendHLSpan(cur, withText(sr.s, string(sr.r)))
		width += ui.Width(string(sr.r))
	}
	i := 0
	for i < len(flat) {
		if flat[i].r == '\n' {
			flush()
			i++
			continue
		}
		// Spaces between words are dropped at a break and folded to one
		// otherwise (textwrap's whitespace handling).
		if flat[i].r == ' ' || flat[i].r == '\t' {
			i++
			continue
		}
		j := i
		wordWidth := 0
		for j < len(flat) && flat[j].r != '\n' && flat[j].r != ' ' && flat[j].r != '\t' {
			wordWidth += ui.Width(string(flat[j].r))
			j++
		}
		if width > 0 {
			if width+1+wordWidth > limit {
				flush()
			} else {
				cur = appendHLSpan(cur, withText(flat[i].s, " "))
				width++
			}
		}
		for k := i; k < j; k++ {
			if width > 0 && width+ui.Width(string(flat[k].r)) > limit {
				flush()
			}
			appendRune(flat[k])
		}
		i = j
	}
	lines = append(lines, cur)
	return lines
}

// renderHLSpans renders one wrapped line's spans. Tokens the theme leaves
// uncolored fall back to fallback (the card's command style).
func renderHLSpans(spans []hlSpan, fallback lipgloss.Style) string {
	var b strings.Builder
	for _, s := range spans {
		if s.text == "" {
			continue
		}
		// Layer the token's attributes onto the fallback rather than
		// replacing it: the fallback carries the row's context (the diff
		// row's background, for one), which must survive the token color.
		st := fallback
		if s.color != "" {
			st = st.Foreground(lipgloss.Color(s.color))
		}
		if s.bold {
			st = st.Bold(true)
		}
		if s.italic {
			st = st.Italic(true)
		}
		b.WriteString(st.Render(s.text))
	}
	return b.String()
}
