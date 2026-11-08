package svgwasm

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func TestValidateStaticSVG(t *testing.T) {
	valid := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><linearGradient id="a"><stop offset="0" stop-color="red"/></linearGradient></defs><rect width="20" height="20" fill="url(#a)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="color"/></defs><style>.x { fill: url(#color); }</style><text x="1" y="2"><tspan>Hello</tspan></text></svg>`,
	}
	for _, svg := range valid {
		if err := Validate([]byte(svg)); err != nil {
			t.Errorf("valid SVG rejected: %v", err)
		}
	}
	invalid := []string{
		`<svg><script>alert(1)</script></svg>`, `<svg onload="bad()"/>`,
		`<svg><image href="https://example.com/a.png"/></svg>`,
		`<svg><foreignObject/></svg>`, `<!DOCTYPE svg><svg/>`,
		`<svg><use href="file:///etc/passwd"/></svg>`,
		`<svg><style>@import url(https://example.com/a.css);</style></svg>`,
		`<svg><rect fill="url(https://example.com/a)"/></svg>`,
		`<svg><rect fill="url(#missing)"/></svg>`, `<svg><g id="x"/><g id="x"/></svg>`,
		`<svg><style>.x{fill:u\72l(https://example.com)}</style></svg>`,
		`<svg/><svg/>`, strings.Repeat("x", maxSVGBytes+1),
	}
	for _, svg := range invalid {
		if err := Validate([]byte(svg)); err == nil {
			t.Errorf("unsafe SVG accepted: %.100q", svg)
		}
	}
}

func TestRenderPinnedSourceWASM(t *testing.T) {
	path := os.Getenv("GOLDER_TEST_SVG_WASM")
	if path == "" {
		t.Skip("set GOLDER_TEST_SVG_WASM to the source-built WASI module")
	}
	module, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40"><rect width="120" height="40" fill="white"/><text x="8" y="28" font-family="sans-serif" font-size="20" fill="black"><tspan>Hello</tspan></text></svg>`)
	out, err := Render(context.Background(), module, svg, goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	black := 0
	for y := 0; y < 40; y++ {
		for x := 0; x < 120; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if r < 30000 {
				black++
			}
		}
	}
	if black < 10 {
		t.Fatal("renderer silently omitted text")
	}
	large := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="4000" height="20"><rect width="4000" height="20" fill="red"/></svg>`)
	data, err := Render(context.Background(), module, large, goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 1280 || config.Height != 7 {
		t.Fatalf("large SVG was not bounded: %+v, %v", config, err)
	}
	tooLarge := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="9000" height="100"><rect width="9000" height="100"/></svg>`)
	if _, err := Render(context.Background(), module, tooLarge, goregular.TTF); err == nil {
		t.Fatal("oversized SVG succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, module, svg, goregular.TTF); err == nil {
		t.Fatal("canceled render succeeded")
	}
}
