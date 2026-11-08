package mygoui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
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
	if len(tex) == 0 || len(tex) > maxTranscriptMathBytes || strings.ContainsAny(tex, "$\x00") || size < 6 || size > 96 {
		return nil
	}
	tex = strings.NewReplacer("\r", " ", "\n", " ").Replace(tex)
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
	if strings.ContainsAny(tex, "^_") || strings.Contains(tex, `\begin{`) {
		if rendered := m.complexExpression(tex, size); rendered != nil {
			return rendered
		}
		return nil
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
