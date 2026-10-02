package tui

import (
	"strings"
	"testing"

	"image/color"
)

// TestDiffRowBgPalettes pins both palettes' washes to codex's constants: the
// dark green/red washes on dark terminals and GitHub's light tints on light
// ones, with context rows unwashed. The colors follow the terminal background
// the Markdown renderer already tracks (SetMarkdownDark).
func TestDiffRowBgPalettes(t *testing.T) {
	toRGBA := func(c color.Color) (uint8, uint8, uint8) {
		r, g, b, _ := c.RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
	}
	orig := syntaxDark()
	t.Cleanup(func() { SetMarkdownDark(orig) })

	SetMarkdownDark(true)
	for _, tc := range []struct {
		kind diffLineKind
		want [3]uint8
	}{
		{diffAdd, [3]uint8{0x21, 0x3A, 0x2B}},
		{diffDel, [3]uint8{0x4A, 0x22, 0x1D}},
	} {
		r, g, b := toRGBA(diffRowBg(tc.kind))
		if got := [3]uint8{r, g, b}; got != tc.want {
			t.Errorf("dark %v bg = %02x%02x%02x, want %02x%02x%02x", tc.kind, got[0], got[1], got[2], tc.want[0], tc.want[1], tc.want[2])
		}
	}

	SetMarkdownDark(false)
	for _, tc := range []struct {
		kind diffLineKind
		want [3]uint8
	}{
		{diffAdd, [3]uint8{0xDA, 0xFB, 0xE1}},
		{diffDel, [3]uint8{0xFF, 0xEB, 0xE9}},
	} {
		r, g, b := toRGBA(diffRowBg(tc.kind))
		if got := [3]uint8{r, g, b}; got != tc.want {
			t.Errorf("light %v bg = %02x%02x%02x, want %02x%02x%02x", tc.kind, got[0], got[1], got[2], tc.want[0], tc.want[1], tc.want[2])
		}
	}

	if diffRowBg(diffContext) != nil || diffRowBg(diffHunk) != nil {
		t.Error("context and hunk rows must not carry a wash")
	}
}

// TestParseUnifiedDiff covers the parser the renderer and the hidden-line
// count share: multi-file splitting, counts excluding the header pair, and
// marker stripping.
func TestParseUnifiedDiff(t *testing.T) {
	diff := "--- a/one.go\n+++ b/one.go\n@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n" +
		"--- a/two.txt\n+++ b/two.txt\n@@ -0,0 +1,2 @@\n+alpha\n+beta\n"
	secs := parseUnifiedDiff(diff)
	if len(secs) != 2 {
		t.Fatalf("sections = %d, want 2\n%+v", len(secs), secs)
	}
	if secs[0].path != "one.go" || secs[0].added != 1 || secs[0].removed != 1 {
		t.Errorf("section 0 = %+v, want one.go (+1 -1)", secs[0])
	}
	if secs[1].path != "two.txt" || secs[1].added != 2 || secs[1].removed != 0 {
		t.Errorf("section 1 = %+v, want two.txt (+2 -0)", secs[1])
	}
	if len(secs[0].lines) != 4 || secs[0].lines[0].kind != diffHunk {
		t.Errorf("section 0 lines = %+v", secs[0].lines)
	}
	if secs[0].lines[2].kind != diffDel || secs[0].lines[2].text != "old" {
		t.Errorf("removed line = %+v, want -old with the marker stripped", secs[0].lines[2])
	}
	if secs[0].lines[1].text != "ctx" {
		t.Errorf("context line = %q, want the leading space stripped", secs[0].lines[1].text)
	}
}

// TestDiffRowsPreserveIndentation is the regression for "the whole diff is
// flush left": diff rows must keep the code's own indentation (tabs expanded
// to a fixed width, matching codex) instead of running through the command
// wrapper, which folds whitespace for shell text.
func TestDiffRowsPreserveIndentation(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,5 +1,5 @@\n func f() {\n-\told()\n+\tnew()\n+\t\tdeep()\n \tctx()\n }\n"
	card := &toolCard{name: "apply_patch", state: cardSuccess, expanded: true, input: map[string]any{"patch": "x"}}
	card.complete(true, "Applied 1 change(s):\n  U x.go", map[string]any{"diff": diff})
	out := card.render(DefaultTheme(), 72)
	plain := stripTCardANSI(out)

	for _, want := range []string{
		"-    old()",      // tab -> four columns after the sign
		"+    new()",      //
		"+        deep()", // two tabs -> eight
		"     ctx()",      // context keeps its indentation too
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("render missing indented row %q\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "\t") {
		t.Errorf("rendered diff must not contain a literal tab (terminal tab stops mangle the gutter)\n%q", plain)
	}
	// Without the fix the body ran flush left: the old-line would read "-old()".
	if strings.Contains(plain, "-old()") {
		t.Errorf("indentation was stripped from the diff body\n%s", plain)
	}
}
