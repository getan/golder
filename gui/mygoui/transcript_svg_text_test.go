package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestLabeledTranscriptSVG(t *testing.T) {
	svg := `<svg viewBox="0 0 160 80"><rect width="160" height="80" fill="#ffffff"/><text x="80" y="45" text-anchor="middle" font-size="16" fill="#000000">Hello 中文</text></svg>`
	img, labels, view := parseLabeledTranscriptSVG(svg)
	if img == nil || len(labels) != 1 || labels[0].text != "Hello 中文" || view != [4]float32{0, 0, 160, 80} {
		t.Fatalf("unexpected labeled SVG: %v %+v %+v", img, labels, view)
	}
	a := testApp()
	item := appsession.Item{ID: "labels", Kind: appsession.ItemAssistant, Done: true, Text: "```svg\n" + svg + "\n```"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 400)
	box, ok := tt.Find("SVG image preview")
	if !ok || box.W <= 0 || box.H <= 0 {
		t.Fatalf("preview not laid out: %+v %v (%q)", box, ok, tt.Texts())
	}
	image := tt.Image()
	background := image.RGBAAt(int(box.X+2), int(box.Y+2))
	var changed bool
	for y := int(box.Y + 25); y < int(box.Y+50); y++ {
		for x := int(box.X + 35); x < int(box.X+125); x++ {
			if image.RGBAAt(x, y) != background {
				changed = true
			}
		}
	}
	if !changed {
		t.Fatal("label did not draw on SVG background")
	}
}

func TestLabeledTranscriptSVGRejectsUnsupportedContent(t *testing.T) {
	for _, source := range []string{
		`<svg viewBox="0 0 10 10"><text x="1" y="5">A<tspan>B</tspan></text></svg>`,
		`<svg viewBox="0 0 10 10"><text x="1" y="5">A</text><rect width="1" height="1"/></svg>`,
		`<svg viewBox="0 0 10 10"><text x="1" y="5" font-family="unknown-font">A</text></svg>`,
		`<svg viewBox="0 0 10 10"><text x="1" y="5" fill="url(https://example.com)">A</text></svg>`,
		`<svg viewBox="0 0 10 10"><text x="1" y="5" onclick="bad()">A</text></svg>`,
		`<svg viewBox="0 0 10 10"><g transform="rotate(30)"><text x="1" y="5">A</text></g></svg>`,
		`<svg viewBox="0 0 10 10"><g transform="scale(2,3)"><text x="1" y="5">A</text></g></svg>`,
		`<svg viewBox="0 0 10 10"><g transform="translate(1,,2)"><text x="1" y="5">A</text></g></svg>`,
	} {
		if img, _, _ := parseLabeledTranscriptSVG(source); img != nil {
			t.Errorf("unsupported SVG rendered: %s", source)
		}
	}
}

func TestLabeledTranscriptSVGAllowsPlainGroup(t *testing.T) {
	source := `<svg viewBox="0 0 100 40"><g><rect width="100" height="40" fill="#ffffff"/><text x="10" y="25" font-family="serif" font-weight="bold" font-style="italic" fill="black">Label</text></g></svg>`
	img, labels, _ := parseLabeledTranscriptSVG(source)
	if img == nil || len(labels) != 1 || labels[0].text != "Label" {
		t.Fatalf("plain group lost: %v %+v", img, labels)
	}
}

func TestLabeledTranscriptSVGTransformsText(t *testing.T) {
	source := `<svg viewBox="0 0 100 100"><g transform="translate(10,20) scale(2)"><text x="5" y="8" font-size="4">Label</text></g></svg>`
	img, labels, _ := parseLabeledTranscriptSVG(source)
	if img == nil || len(labels) != 1 || labels[0].x != 20 || labels[0].y != 36 || labels[0].size != 8 {
		t.Fatalf("incorrect transformed label: %v %+v", img, labels)
	}
}
