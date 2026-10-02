package tui

// Diff rendering for tool cards, codex-style: a header line per file carrying
// the change counts, then the lines themselves with their syntax colors laid
// over a diff background — green for additions, red for removals — so a change
// reads at a glance instead of as a wall of one color. The raw ---/+++ file
// headers are folded into the counts header.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/color"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// diffTab is how a tab renders inside diff content: a fixed four columns,
// matching codex's TAB_REPLACEMENT. A literal tab would advance to the
// terminal's physical tab stop — which already includes the card's gutter —
// so every indented row would land at a different column than the code it is
// supposed to mirror.
const diffTab = "    "

// diffLineKind classifies one line of a unified diff.
type diffLineKind int

const (
	diffContext diffLineKind = iota
	diffAdd
	diffDel
	diffHunk
)

// diffLine is one parsed diff line; text excludes the leading marker so the
// renderer can add its own gutter.
type diffLine struct {
	kind diffLineKind
	text string
}

// diffSection is one file's slice of a unified diff.
type diffSection struct {
	path    string
	lines   []diffLine
	added   int
	removed int
}

// parseUnifiedDiff splits a unified diff — one or more concatenated file
// diffs, as apply_patch emits — into per-file sections with their change
// counts. The ---/+++ header pair starts a section and contributes no lines.
func parseUnifiedDiff(diff string) []diffSection {
	var sections []diffSection
	var cur *diffSection
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			sections = append(sections, diffSection{path: diffHeaderPath(line)})
			cur = &sections[len(sections)-1]
		case strings.HasPrefix(line, "+++ "), line == "":
			// The +++ half of the header pair, and trailing split artifacts.
		case cur == nil:
			// A diff with no file header: nothing to attach lines to.
		case strings.HasPrefix(line, "@@"):
			cur.lines = append(cur.lines, diffLine{kind: diffHunk, text: line})
		case strings.HasPrefix(line, "+"):
			cur.added++
			cur.lines = append(cur.lines, diffLine{kind: diffAdd, text: line[1:]})
		case strings.HasPrefix(line, "-"):
			cur.removed++
			cur.lines = append(cur.lines, diffLine{kind: diffDel, text: line[1:]})
		default:
			cur.lines = append(cur.lines, diffLine{kind: diffContext, text: strings.TrimPrefix(line, " ")})
		}
	}
	return sections
}

// diffHeaderPath reads the path out of a "--- a/path" header.
func diffHeaderPath(line string) string {
	p := strings.TrimPrefix(line, "--- ")
	return strings.TrimPrefix(p, "a/")
}

// diffRowBg is codex's row background for a changed line: a dark green/red
// wash on dark terminals (its DARK_TC_* constants), GitHub's light tints on
// light ones. Context and hunk rows get none.
func diffRowBg(kind diffLineKind) color.Color {
	dark := syntaxDark()
	switch kind {
	case diffAdd:
		if dark {
			return lipgloss.Color("#213A2B")
		}
		return lipgloss.Color("#dafbe1")
	case diffDel:
		if dark {
			return lipgloss.Color("#4A221D")
		}
		return lipgloss.Color("#ffebe9")
	}
	return nil
}

// diffRenderCache memoizes a rendered diff per (content, width, palette).
// Tool cards are re-rendered on every reflow — and a streaming turn reflows
// on every delta — so without this a large diff would re-tokenize its lines
// continuously.
const diffRenderCacheMax = 64

var (
	diffRenderMu    sync.Mutex
	diffRenderCache = map[string]string{}
)

func diffRenderKey(diff string, inner int, dark bool) string {
	sum := sha256.Sum256([]byte(diff))
	return fmt.Sprintf("%d|%v|%s", inner, dark, hex.EncodeToString(sum[:]))
}

// diffTotals sums a diff's change counts across its files.
func diffTotals(diff string) (added, removed int) {
	for _, sec := range parseUnifiedDiff(diff) {
		added += sec.added
		removed += sec.removed
	}
	return added, removed
}

// renderDiff renders the card's unified diff for the given content width.
// Headers are rendered only for a multi-file diff: with one file the card's
// headline already carries the path and the counts (codex's rule), so a
// second header line would just repeat it at a shallower indent.
func renderDiff(theme Theme, diff string, inner int) string {
	key := diffRenderKey(diff, inner, syntaxDark())
	diffRenderMu.Lock()
	cached, ok := diffRenderCache[key]
	diffRenderMu.Unlock()
	if ok {
		return cached
	}

	sections := parseUnifiedDiff(diff)
	var b strings.Builder
	for i, sec := range sections {
		if len(sections) > 1 {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("\n" + renderDiffHeader(theme, sec))
		}
		for _, ln := range sec.lines {
			b.WriteString("\n" + renderDiffRow(theme, sec.path, ln, inner))
		}
	}
	out := b.String()

	diffRenderMu.Lock()
	if len(diffRenderCache) >= diffRenderCacheMax {
		diffRenderCache = map[string]string{}
	}
	diffRenderCache[key] = out
	diffRenderMu.Unlock()
	return out
}

// renderDiffHeader renders one file's header in codex's shape: the └ gutter,
// the path in the card's emphasis, then the change counts with additions green
// and removals red.
func renderDiffHeader(theme Theme, sec diffSection) string {
	counts := theme.System.Render("(") +
		theme.DiffAdd.Render(fmt.Sprintf("+%d", sec.added)) +
		theme.System.Render(" ") +
		theme.DiffDel.Render(fmt.Sprintf("-%d", sec.removed)) +
		theme.System.Render(")")
	return theme.System.Render("  └ ") + theme.ToolCmd.Bold(true).Render(sec.path) + " " + counts
}

// renderDiffRow renders one diff line: hunk markers dim, context lines in the
// body color, and changed lines with their syntax colors over the diff
// background, padded to the full content width so the wash reads as one block
// (codex does the same).
func renderDiffRow(theme Theme, path string, ln diffLine, inner int) string {
	const indent = "    "
	// Code indentation is the point of a diff row, so tabs are expanded to a
	// fixed width and every rune is preserved (see wrapDiffSpans).
	text := strings.ReplaceAll(ln.text, "\t", diffTab)
	if ln.kind == diffHunk {
		return theme.DiffHunk.Render(WrapToWidth(indent+text, inner))
	}

	base := theme.ToolCmd
	sign, signStyle := " ", theme.System
	switch ln.kind {
	case diffAdd:
		sign, signStyle = "+", theme.DiffAdd
	case diffDel:
		sign, signStyle = "-", theme.DiffDel
	default:
		// Context keeps the code colors but no wash, so the changed lines
		// stay the thing the eye lands on.
		return theme.System.Render(WrapToWidth(indent+" "+text, inner))
	}
	bg := diffRowBg(ln.kind)
	base = base.Background(bg)

	// Every row starts at the same column (indent + one sign/marker cell), so
	// first and continuation share one wrap budget.
	limit := max(1, inner-len(indent)-1)
	rows := wrapDiffSpans(highlightCodeLine(path, text, syntaxDark()), limit)
	pad := lipgloss.NewStyle().Width(inner).Background(bg)
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		mark := sign
		if i > 0 {
			// Continuations align under the content, not under a repeated sign.
			mark = " "
		}
		line := indent + signStyle.Background(bg).Render(mark) + renderHLSpans(row, base)
		b.WriteString(pad.Render(line))
	}
	return b.String()
}

// wrapDiffSpans wraps styled spans to width columns for diff content. Unlike
// the command wrapper, which folds whitespace runs for shell text, this one
// preserves every rune — leading indentation included — and only breaks when
// the next rune would overflow. Tabs arrive already expanded (diffTab).
func wrapDiffSpans(spans []hlSpan, width int) [][]hlSpan {
	if width < 1 {
		width = 1
	}
	var lines [][]hlSpan
	var cur []hlSpan
	w := 0
	for _, s := range spans {
		for _, r := range s.text {
			rw := ui.Width(string(r))
			if w > 0 && w+rw > width {
				lines = append(lines, cur)
				cur, w = nil, 0
			}
			cur = appendHLSpan(cur, withText(s, string(r)))
			w += rw
		}
	}
	return append(lines, cur)
}
