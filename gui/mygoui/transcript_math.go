package mygoui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"codeberg.org/go-latex/latex/drawtex"
	"codeberg.org/go-latex/latex/drawtex/drawimg"
	"codeberg.org/go-latex/latex/mtex"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

const (
	maxTranscriptMathBytes  = 512
	maxTranscriptMathPixels = 4_000_000
)

type mathImage struct {
	bitmap *ui.Bitmap
	mask   *image.NRGBA
	w, h   float32
}

type transcriptMath struct {
	cache  map[string]*mathImage
	pixels int
}

func newTranscriptMath() *transcriptMath { return &transcriptMath{cache: make(map[string]*mathImage)} }

type boundedMathRenderer struct{ dst *bytes.Buffer }

func (r boundedMathRenderer) Render(w, h, dpi float64, cnv *drawtex.Canvas) error {
	if math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w <= 0 || h <= 0 || w*dpi > 2048 || h*dpi > 512 {
		return errors.New("formula too large")
	}
	return drawimg.NewRenderer(r.dst).Render(w, h, dpi, cnv)
}

// render limits the parser's input, caches results per message, and catches
// renderer panics so a malformed response cannot crash the window.
func (m *transcriptMath) render(tex string, size float32) (result *mathImage) {
	if len(tex) == 0 || len(tex) > maxTranscriptMathBytes || strings.ContainsAny(tex, "$\x00\r\n") || size < 6 || size > 96 {
		return nil
	}
	depth := 0
	for _, r := range tex {
		switch r {
		case '{':
			depth++
			if depth > 16 {
				return nil
			}
		case '}':
			depth--
			if depth < 0 {
				return nil
			}
		}
	}
	if depth != 0 {
		return nil
	}
	key := tex + "\x00" + string(rune(size*16))
	if found, ok := m.cache[key]; ok {
		return found
	}
	if len(m.cache) >= 64 {
		return nil
	}
	defer func() {
		if recover() != nil {
			result = nil
		}
		if result != nil {
			pixels := result.mask.Bounds().Dx() * result.mask.Bounds().Dy()
			if pixels > maxTranscriptMathPixels-m.pixels {
				result = nil
			} else {
				m.pixels += pixels
			}
		}
		m.cache[key] = result
	}()
	if strings.ContainsAny(tex, "^_") {
		return m.scriptedExpression(tex, size)
	}
	var out bytes.Buffer
	if err := mtex.Render(boundedMathRenderer{&out}, "$"+tex+"$", float64(size)*.75, 192, nil); err != nil {
		return nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(out.Bytes()))
	if err != nil {
		return nil
	}
	bounds := decoded.Bounds()
	mask := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := decoded.At(x, y).RGBA()
			mask.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: uint8(alpha >> 8)})
		}
	}
	return &mathImage{bitmap: ui.NewBitmap(mask), mask: mask, w: float32(bounds.Dx()) / 2, h: float32(bounds.Dy()) / 2}
}

func mathASCIIAtom(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// go-latex currently panics on scripts. For plain expressions with one ASCII
// base per script, compose its glyphs instead. Unsupported TeX stays source.
func (m *transcriptMath) scriptedExpression(tex string, size float32) *mathImage {
	var parts []*mathImage
	appendText := func(text string) bool {
		if text == "" {
			return true
		}
		img := m.render(text, size)
		if img == nil {
			return false
		}
		parts = append(parts, img)
		return true
	}
	start := 0
	for i := 0; i < len(tex); i++ {
		if tex[i] != '^' && tex[i] != '_' {
			if !mathASCIIAtom(tex[i]) && !strings.ContainsRune(" +-=/().", rune(tex[i])) {
				return nil
			}
			continue
		}
		if i <= start || !mathASCIIAtom(tex[i-1]) || !appendText(tex[start:i-1]) {
			return nil
		}
		end := i + 1
		if end >= len(tex) {
			return nil
		}
		var script string
		if tex[end] == '{' {
			closeAt := strings.IndexByte(tex[end+1:], '}')
			if closeAt < 0 {
				return nil
			}
			closeAt += end + 1
			script, end = tex[end+1:closeAt], closeAt+1
		} else {
			script, end = tex[end:end+1], end+1
		}
		if len(script) == 0 || len(script) > 8 {
			return nil
		}
		for j := range len(script) {
			if !mathASCIIAtom(script[j]) {
				return nil
			}
		}
		part := m.simpleScript(tex[i-1:i], script, tex[i] == '^', size)
		if part == nil {
			return nil
		}
		parts = append(parts, part)
		start, i = end, end-1
	}
	if !appendText(tex[start:]) || len(parts) == 0 {
		return nil
	}
	w, h := 0, 0
	for _, part := range parts {
		w += part.mask.Bounds().Dx()
		h = max(h, part.mask.Bounds().Dy())
	}
	if w > 2048 || h > 512 {
		return nil
	}
	mask := image.NewNRGBA(image.Rect(0, 0, w, h))
	x := 0
	for _, part := range parts {
		b := part.mask.Bounds()
		draw.Draw(mask, image.Rect(x, h-b.Dy(), x+b.Dx(), h), part.mask, b.Min, draw.Over)
		x += b.Dx()
	}
	return &mathImage{bitmap: ui.NewBitmap(mask), mask: mask, w: float32(w) / 2, h: float32(h) / 2}
}

func (m *transcriptMath) simpleScript(baseText, scriptText string, superscript bool, size float32) *mathImage {
	base := m.render(baseText, size)
	script := m.render(scriptText, size*.7)
	if base == nil || script == nil || base.mask == nil || script.mask == nil {
		return nil
	}
	b, s := base.mask.Bounds(), script.mask.Bounds()
	baseY, scriptY := 0, 0
	if superscript {
		baseY = s.Dy() / 2
	} else {
		scriptY = b.Dy() * 2 / 3
	}
	mask := image.NewNRGBA(image.Rect(0, 0, b.Dx()+s.Dx(), max(baseY+b.Dy(), scriptY+s.Dy())))
	draw.Draw(mask, image.Rect(0, baseY, b.Dx(), baseY+b.Dy()), base.mask, b.Min, draw.Over)
	draw.Draw(mask, image.Rect(b.Dx(), scriptY, b.Dx()+s.Dx(), scriptY+s.Dy()), script.mask, s.Min, draw.Over)
	return &mathImage{bitmap: ui.NewBitmap(mask), mask: mask, w: float32(mask.Bounds().Dx()) / 2, h: float32(mask.Bounds().Dy()) / 2}
}

func drawMath(p *ui.Painter, img *mathImage, r ui.Rect) {
	p.ImageRegion(img.bitmap, ui.Rect{}, r, ui.ImageOptions{Tint: p.Theme().Text})
}

func (m *transcriptMath) inline(tex string, size float32) (markdown.InlineObject, bool) {
	img := m.render(tex, size)
	if img == nil {
		return markdown.InlineObject{}, false
	}
	return markdown.InlineObject{Width: img.w, Height: img.h * .8, Depth: img.h * .2, Draw: func(p *ui.Painter, r ui.Rect) {
		drawMath(p, img, r)
	}}, true
}

func (m *transcriptMath) block(c *ui.Context, tex string) {
	tex = strings.TrimSpace(tex)
	img := m.render(tex, 18)
	if img == nil {
		ui.Text(c, tex).Font("monospace").Selectable().Wrap()
		return
	}
	ui.ScrollHorizontal(c).AlignSelf(ui.Stretch).Children(func() {
		ui.Box(c).Width(img.w).Height(img.h).Label("Math formula preview").Draw(func(p *ui.Painter, r ui.Rect) { drawMath(p, img, r) })
	})
	ui.Text(c, tex).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable().Wrap()
}
