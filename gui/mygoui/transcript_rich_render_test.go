package mygoui

import (
	"bytes"
	"compress/gzip"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font/gofont/goregular"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/gui/svgwasm"
)

func TestRichPreviewFontCoverage(t *testing.T) {
	if !fontSupports(goregular.TTF, textRunes("Hello 123")) {
		t.Fatal("bundled font does not support ASCII")
	}
	if fontSupports(goregular.TTF, textRunes("中文")) {
		t.Fatal("bundled font unexpectedly claimed CJK coverage")
	}
	if !fontSupportsSVG(goregular.TTF, `<svg><text><tspan>Hello</tspan></text></svg>`) {
		t.Fatal("ASCII SVG text rejected")
	}
	if fontSupportsSVG(goregular.TTF, `<svg><text><tspan>中文</tspan></text></svg>`) {
		t.Fatal("missing CJK SVG text accepted")
	}
}

func TestRichPreviewSourceValidation(t *testing.T) {
	if _, err := renderRichWithModules(context.Background(), `<svg><script>bad()</script></svg>`, false, nil, nil); err == nil {
		t.Fatal("unsafe SVG accepted")
	}
	if _, err := renderRichWithModules(context.Background(), strings.Repeat("x", (512<<10)+1), false, nil, nil); err == nil {
		t.Fatal("oversized SVG accepted")
	}
}

func TestRendererModuleRejectsCompressedBomb(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(make([]byte, maxRendererModuleBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bomb.wasm.gz")
	if err := os.WriteFile(path, compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRendererModule(path); err == nil {
		t.Fatal("oversized decompressed WASM accepted")
	}
}

// These modules are produced from the checked-in Rust source and the pinned
// upstream Mermaid release. The test never downloads modules itself.
func TestRichPreviewWithPinnedModules(t *testing.T) {
	svgPath, mermaidPath := os.Getenv("GOLDER_TEST_SVG_WASM"), os.Getenv("GOLDER_TEST_MERMAID_WASM")
	if svgPath == "" || mermaidPath == "" {
		t.Skip("set both GOLDER_TEST_SVG_WASM and GOLDER_TEST_MERMAID_WASM")
	}
	svgModule, err := os.ReadFile(svgPath)
	if err != nil {
		t.Fatal(err)
	}
	mermaidModule, err := os.ReadFile(mermaidPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source string
		mermaid      bool
	}{
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="60"><rect width="160" height="60" fill="white"/><g transform="rotate(5 50 30)"><text x="15" y="35" font-size="20"><tspan>Hello</tspan></text></g></svg>`, false},
		{"flowchart", "flowchart LR\nA --> B\nB --> A", true},
		{"sequence", "sequenceDiagram\nAlice->>Bob: Hello\nloop Repeat\nBob-->>Alice: Done\nend", true},
		{"class", "classDiagram\nAnimal <|-- Dog", true},
		{"state", "stateDiagram-v2\n[*] --> Idle\nIdle --> [*]", true},
		{"er", "erDiagram\nCUSTOMER ||--o{ ORDER : places", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := renderRichWithModules(context.Background(), tc.source, tc.mermaid, svgModule, mermaidModule)
			if err != nil || img == nil {
				t.Fatalf("rich preview failed: %v", err)
			}
			w, h := img.Size()
			if w <= 0 || h <= 0 {
				t.Fatalf("invalid image size %dx%d", w, h)
			}
		})
	}
	t.Run("chinese", func(t *testing.T) {
		font, err := fontForText("开始结束")
		if err != nil {
			t.Skipf("no suitable system CJK font: %v", err)
		}
		img, err := renderRichWithModules(context.Background(), "flowchart LR\nA[开始] --> B[结束]\nA --> C[其他]\nC --> B", true, svgModule, mermaidModule)
		if err != nil || img == nil {
			t.Fatalf("CJK Mermaid preview failed: %v", err)
		}
		svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40"><rect width="120" height="40" fill="white"/><text x="8" y="28" font-family="sans-serif" font-size="20" fill="black">开始结束</text></svg>`)
		data, err := svgwasm.Render(context.Background(), svgModule, svg, font)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		dark := 0
		for y := 0; y < 40; y++ {
			for x := 0; x < 120; x++ {
				r, _, _, _ := rendered.At(x, y).RGBA()
				if r < 30000 {
					dark++
				}
			}
		}
		if dark < 10 {
			t.Fatal("CJK text was silently omitted")
		}
	})
	t.Run("transcript", func(t *testing.T) {
		dir := t.TempDir()
		assets := filepath.Join(dir, "resources", "renderers")
		if err := os.MkdirAll(assets, 0755); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"svg.wasm.gz": svgModule, "mermaid.wasm.gz": mermaidModule} {
			var compressed bytes.Buffer
			zw := gzip.NewWriter(&compressed)
			if _, err := zw.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(assets, name), compressed.Bytes(), 0644); err != nil {
				t.Fatal(err)
			}
		}
		oldDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		defer os.Chdir(oldDir)
		fileSVG := `<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40"><text x="5" y="25"><tspan>File</tspan></text></svg>`
		if err := os.WriteFile(filepath.Join(dir, "file.svg"), []byte(fileSVG), 0644); err != nil {
			t.Fatal(err)
		}
		a := testApp()
		a.project = dir
		svg := appsession.Item{ID: "svg-rich", Kind: appsession.ItemAssistant, Done: true, Text: "```svg\n<svg width=\"160\" height=\"60\"><text x=\"15\" y=\"35\"><tspan>Hello</tspan></text></svg>\n```"}
		mermaid := appsession.Item{ID: "mermaid-rich", Kind: appsession.ItemAssistant, Done: true, Text: "```mermaid\nclassDiagram\nAnimal <|-- Dog\n```"}
		image := appsession.Item{ID: "svg-image", Kind: appsession.ItemAssistant, Done: true, Text: "![diagram attachment](file.svg)"}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Children(func() {
				a.itemView(c, svg)
				a.itemView(c, mermaid)
				a.itemView(c, image)
			})
		}, 800, 800)
		deadline := time.Now().Add(30 * time.Second)
		for {
			tt.Frame()
			_, svgOK := tt.Find("SVG preview image")
			_, mermaidOK := tt.Find("Mermaid preview image")
			imageBox, imageOK := tt.Find("diagram attachment")
			imageOK = imageOK && imageBox.W == 80 && imageBox.H == 40
			if svgOK && mermaidOK && imageOK {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing async rich previews: svg=%v mermaid=%v image=%v texts=%q", svgOK, mermaidOK, imageOK, tt.Texts())
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}
