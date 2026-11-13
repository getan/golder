package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/displaytext"
)

func TestGUIToolPreviewFormatsJSONAndWrapsWithoutChangingStoredResult(t *testing.T) {
	a := testApp()
	raw := `{"text":"` + strings.Repeat("中文 long value ", 25) + `","n":9007199254740993}`
	item := exploreTestTool("json", "browser", map[string]any{"action": "snapshot"}, raw)
	a.toolOpen[item.ID] = true
	tt := ui.NewTester(func(c *ui.Context) { a.toolCard(c, item) }, 320, 900)
	tt.Frame()
	formatted := displaytext.PrettyJSON(raw)
	box, ok := tt.Find(formatted)
	if !ok || box.W > 320 || box.H < 50 {
		t.Fatalf("formatted preview did not wrap in narrow view: found=%t box=%+v texts=%q", ok, box, tt.Texts())
	}
	if item.Tool.Output != raw {
		t.Fatal("display formatting changed stored tool output")
	}
}

func TestGUIReadPreviewFormatsNumberedJSON(t *testing.T) {
	a := testApp()
	item := exploreTestTool("json-read", "read", map[string]any{"path": "fixture.json"},
		"     1\t{\"name\":\"golder\",\"items\":[1,2]}\n")
	a.toolOpen[item.ID] = true
	tt := ui.NewTester(func(c *ui.Context) { a.toolCard(c, item) }, 800, 900)
	tt.Frame()
	for _, want := range []string{`"name": "golder"`, `"items": [`, `1`, `2`} {
		if !tt.HasText(want) {
			t.Fatalf("GUI read preview is missing %q: %q", want, tt.Texts())
		}
	}
}
