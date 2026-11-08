package mygoui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

const simpleTranscriptSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20"><rect width="40" height="20" fill="#ff0000"/></svg>`

func TestParseTranscriptSVG(t *testing.T) {
	tests := []struct {
		name, source string
		ok           bool
	}{
		{"simple", simpleTranscriptSVG, true},
		{"path", `<svg viewBox="0 0 10 10"><path d="M0 0L10 10" stroke="blue"/></svg>`, true},
		{"gradient", `<svg viewBox="0 0 10 10"><defs><linearGradient id="g"><stop offset="0" stop-color="#f00"/><stop offset="1" stop-color="#00f"/></linearGradient></defs><rect width="10" height="10" fill="url(#g)"/></svg>`, true},
		{"missing gradient", `<svg viewBox="0 0 10 10"><rect width="10" height="10" fill="url(#missing)"/></svg>`, false},
		{"empty gradient", `<svg viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect width="10" height="10" fill="url(#g)"/></svg>`, false},
		{"duplicate gradient", `<svg viewBox="0 0 10 10"><defs><linearGradient id="g"/><linearGradient id="g"/></defs></svg>`, false},
		{"text not supported", `<svg viewBox="0 0 10 10"><text x="0" y="5">label</text></svg>`, false},
		{"script", `<svg viewBox="0 0 10 10"><script>alert(1)</script></svg>`, false},
		{"external image", `<svg viewBox="0 0 10 10"><image href="https://example.com/a.png"/></svg>`, false},
		{"external paint", `<svg viewBox="0 0 10 10"><rect fill="url(https://example.com/a)"/></svg>`, false},
		{"doctype", `<!DOCTYPE svg><svg viewBox="0 0 10 10"/>`, false},
		{"multiple roots", `<svg viewBox="0 0 10 10"/><svg viewBox="0 0 10 10"/>`, false},
		{"default size", `<svg><rect width="10" height="10"/></svg>`, true},
		{"broken", `<svg viewBox="0 0 10 10"><rect></svg>`, false},
		{"oversize", `<svg>` + strings.Repeat(" ", maxTranscriptSVGBytes) + `</svg>`, false},
		{"too many elements", `<svg viewBox="0 0 10 10">` + strings.Repeat(`<rect/>`, maxTranscriptSVGElements) + `</svg>`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img, err := parseTranscriptSVG(tc.source)
			if (err == nil && img != nil) != tc.ok {
				t.Fatalf("image=%v err=%v, want success %v", img, err, tc.ok)
			}
		})
	}
}

func TestTranscriptSVGPreviewAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, language, source, want string
	}{
		{"preview", "svg", simpleTranscriptSVG, "SVG preview"},
		{"xml preview", "xml", simpleTranscriptSVG, "SVG preview"},
		{"unsupported", "svg", `<svg viewBox="0 0 10 10"><text>label</text></svg>`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			item := appsession.Item{ID: "svg", Kind: appsession.ItemAssistant, Done: true, Text: "```" + tc.language + "\n" + tc.source + "\n```\n"}
			tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 600)
			if tc.want != "" && !tt.HasText(tc.want) {
				t.Fatalf("missing %q: %q", tc.want, tt.Texts())
			}
			if tc.want == "" && !tt.HasText("Rendering SVG preview") && !tt.HasText("SVG preview unavailable") {
				t.Fatalf("missing preview status: %q", tt.Texts())
			}
			if !tt.HasText("SVG source") || tt.HasText("Copy") {
				t.Fatalf("source should start collapsed: %q", tt.Texts())
			}
			if err := tt.Click("SVG source"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(250 * time.Millisecond)
			if !tt.HasText("Copy") {
				t.Fatalf("source did not expand: %q", tt.Texts())
			}
			if err := tt.Click("SVG source"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(250 * time.Millisecond)
			if tt.HasText("Copy") {
				t.Fatalf("source did not collapse again: %q", tt.Texts())
			}
			if tc.source == simpleTranscriptSVG {
				r, ok := tt.Find("SVG image preview")
				if !ok || r.W <= 0 || r.H <= 0 {
					t.Fatalf("SVG image has no layout: %v, %v", r, ok)
				}
				pixel := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
				if pixel.R < 240 || pixel.G > 20 || pixel.B > 20 {
					t.Fatalf("SVG image center is not red: %v", pixel)
				}
			}
		})
	}
}

func TestStreamingSVGSourceStartsCollapsed(t *testing.T) {
	a := testApp()
	item := appsession.Item{ID: "streaming-svg", Kind: appsession.ItemAssistant, Text: "```svg\n" + simpleTranscriptSVG + "\n"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 600)
	if !tt.HasText("SVG source") || !tt.HasText("preview will appear") || tt.HasText("Copy") {
		t.Fatalf("streaming SVG source was not collapsed: %q", tt.Texts())
	}
	if err := tt.Click("SVG source"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("Copy") {
		t.Fatalf("streaming SVG source could not be expanded: %q", tt.Texts())
	}
}

func TestNonSVGXMLKeepsNormalCodeBlock(t *testing.T) {
	a := testApp()
	item := appsession.Item{ID: "xml", Kind: appsession.ItemAssistant, Done: true, Text: "```xml\n<config><value>hello</value></config>\n```\n"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 600)
	if !tt.HasText("Copy") || tt.HasText("SVG source") {
		t.Fatalf("non-SVG XML was unexpectedly folded: %q", tt.Texts())
	}
}

func TestSVGMarkupIdentification(t *testing.T) {
	for _, source := range []string{simpleTranscriptSVG, `<?xml version="1.0"?><svg viewBox="0 0 1 1"/>`, `<!-- diagram --><svg/>`} {
		if !isSVGMarkup(source) {
			t.Errorf("SVG not recognized: %q", source)
		}
	}
	for _, source := range []string{`<html><svg/></html>`, `<!DOCTYPE svg><svg/>`, `<script>alert(1)</script>`, `text<svg/>`} {
		if isSVGMarkup(source) {
			t.Errorf("non-SVG recognized: %q", source)
		}
	}
}
