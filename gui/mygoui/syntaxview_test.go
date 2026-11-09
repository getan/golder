package mygoui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func spanText(spans []ui.Span) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

func TestSyntaxFileLinePreservesTextAndHighlightsLanguages(t *testing.T) {
	for _, tc := range []struct {
		path string
		text string
	}{
		{"main.go", "\tfunc main() { return \"你好\" }"},
		{"script.py", "def greet(name): return \"hello\""},
		{"app.js", "const value = 42;"},
		{"main.rs", "fn main() { let value = 42; }"},
		{"config.json", `{"enabled": true, "count": 42}`},
	} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark=%t", tc.path, dark), func(t *testing.T) {
				spans := syntaxFileLine(tc.path, tc.text, dark)
				if got := spanText(spans); got != tc.text {
					t.Fatalf("text = %q, want %q", got, tc.text)
				}
				colors := map[ui.Color]bool{}
				for _, span := range spans {
					if span.Color.A != 0 {
						colors[span.Color] = true
					}
				}
				if len(colors) < 2 {
					t.Fatalf("expected syntax colors, got %d: %+v", len(colors), spans)
				}
			})
		}
	}
}

func TestSyntaxFileLineFallbackAndTheme(t *testing.T) {
	text := `func main() { println("hello") }`
	light := syntaxFileLine("main.go", text, false)
	dark := syntaxFileLine("main.go", text, true)
	if spanText(light) != text || spanText(dark) != text || len(light) == 0 || len(dark) == 0 {
		t.Fatal("highlighting changed the code")
	}
	if light[0].Color == dark[0].Color {
		t.Fatalf("light and dark palettes are identical: %+v / %+v", light[0], dark[0])
	}
	for _, tc := range []struct{ path, text string }{
		{"unknown.golder-unknown-extension", "  arbitrary text\t你好"},
		{"main.go", strings.Repeat("a", syntaxMaxLineBytes+1)},
		{"", text},
	} {
		spans := syntaxFileLine(tc.path, tc.text, true)
		if len(spans) != 1 || spans[0].Text != tc.text || spans[0].Color.A != 0 {
			t.Fatalf("plain fallback for %q: %+v", tc.path, spans)
		}
	}
}

func TestNumberedReadSpansPreserveOutputAndDiagnostics(t *testing.T) {
	output := "     8\tfunc main() {\n     9\t\tfmt.Println(\"你好\")\n    10\t}\n\n... (output truncated at 2000 lines; use offset to read more)"
	gutter := ui.RGB(1, 2, 3)
	spans, ok := numberedReadSpans("main.go", output, true, gutter)
	if !ok || spanText(spans) != output {
		t.Fatalf("read output changed: ok=%t got=%q", ok, spanText(spans))
	}
	if spans[0].Text != "     8\t" || spans[0].Color != gutter {
		t.Fatalf("line number not styled as gutter: %+v", spans[0])
	}
	colored := false
	for _, span := range spans {
		if span.Color.A != 0 && span.Color != gutter {
			colored = true
		}
	}
	if !colored {
		t.Fatal("source code has no syntax colors")
	}
	if _, ok := numberedReadSpans("main.go", "read: file not found", true, gutter); ok {
		t.Fatal("diagnostic was mistaken for numbered source")
	}
}

func TestGrepResultSpansPreserveMixedResults(t *testing.T) {
	output := "2 match(es) for \"func\"\ncmd/main.go:12:func main() {\ncmd/main.go-13-  context\nC:\\repo\\other.py:9:def greet():\n... (truncated)"
	gutter := ui.RGB(1, 2, 3)
	spans, ok := grepResultSpans(output, true, gutter)
	if !ok || spanText(spans) != output {
		t.Fatalf("grep output changed: ok=%t got=%q", ok, spanText(spans))
	}
	var prefixes int
	colored := false
	for _, span := range spans {
		if span.Color == gutter {
			prefixes++
		} else if span.Color.A != 0 {
			colored = true
		}
	}
	if prefixes != 2 || !colored {
		t.Fatalf("grep styles: prefixes=%d colored=%t spans=%+v", prefixes, colored, spans)
	}
	if _, ok := grepResultSpans("2 file(s) matching \"main\"\nmain.go\nother.go", true, gutter); ok {
		t.Fatal("file-only output was mistaken for source matches")
	}
}

func TestPatchResultSpansKeepDiffAndNotes(t *testing.T) {
	output := "Applied 1 change(s):\n  D internal/demo.go\n" + samplePatchDiff + "\nnote: unchanged"
	add, remove := ui.RGB(1, 2, 3), ui.RGB(4, 5, 6)
	spans, ok := patchResultSpans(output, true, add, remove)
	if !ok || spanText(spans) != output {
		t.Fatalf("patch output changed: ok=%t got=%q", ok, spanText(spans))
	}
	var added, removed, code bool
	for _, span := range spans {
		switch {
		case span.Text == "+" && span.Color == add:
			added = true
		case span.Text == "-" && span.Color == remove:
			removed = true
		case span.Color.A != 0 && span.Color != add && span.Color != remove:
			code = true
		}
	}
	if !added || !removed || !code {
		t.Fatalf("missing diff styles: added=%t removed=%t code=%t", added, removed, code)
	}
	if _, ok := patchResultSpans("Applied 1 change(s):\n  U main.go", true, add, remove); ok {
		t.Fatal("summary-only output was mistaken for a diff")
	}
}

func TestReadToolCardKeepsNumberedText(t *testing.T) {
	a := testApp()
	item := appsession.Item{
		ID: "read", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{Name: "read", Input: map[string]any{"path": "main.go"}, Output: "     1\tfunc main() {\n     2\t\tprintln(\"hello\")\n     3\t}\n"},
	}
	a.toolOpen["read"] = true
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 1200, 800)
	for _, want := range []string{"read · main.go · done", "func main()", `println("hello")`} {
		if !tt.HasText(want) {
			t.Fatalf("read card missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestSyntaxCacheIsBounded(t *testing.T) {
	for i := range syntaxCacheEntries + 80 {
		syntaxFileLine("main.go", fmt.Sprintf("var item%d = %d", i, i), true)
	}
	syntaxCache.Lock()
	defer syntaxCache.Unlock()
	if len(syntaxCache.lines) > syntaxCacheEntries || syntaxCache.bytes > syntaxCacheBytes {
		t.Fatalf("cache exceeded budget: %d entries, %d bytes", len(syntaxCache.lines), syntaxCache.bytes)
	}
}
