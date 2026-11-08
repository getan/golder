package mygoui

import (
	"image"
	"image/color"
	"image/draw"
	"strings"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/vector"
)

// complexMathParser is deliberately a bounded math-expression parser, not a
// TeX interpreter. Unknown commands and constructs fail closed to source.
type complexMathParser struct {
	source string
	pos    int
	tokens int
	pixels int
	math   *transcriptMath
}

func (m *transcriptMath) complexExpression(tex string, size float32) *mathImage {
	p := &complexMathParser{source: tex, math: m}
	result := p.expression(size, 0, false)
	if result == nil || p.pos != len(tex) {
		return nil
	}
	return result
}

func (p *complexMathParser) expression(size float32, depth int, group bool) *mathImage {
	if depth > 16 || size < 6 {
		return nil
	}
	var parts []*mathImage
	for p.pos < len(p.source) {
		if p.source[p.pos] == '}' {
			if !group {
				return nil
			}
			p.pos++
			return p.bounded(joinMathMasks(parts))
		}
		if p.source[p.pos] == ' ' || p.source[p.pos] == '\t' {
			p.pos++
			continue
		}
		base := p.bounded(p.atom(size, depth))
		if base == nil {
			return nil
		}
		var sub, sup *mathImage
		for p.pos < len(p.source) && (p.source[p.pos] == '_' || p.source[p.pos] == '^') {
			op := p.source[p.pos]
			p.pos++
			if op == '_' && sub != nil || op == '^' && sup != nil {
				return nil
			}
			script := p.bounded(p.argument(size*.7, depth+1))
			if script == nil {
				return nil
			}
			if op == '_' {
				sub = script
			} else {
				sup = script
			}
		}
		if sub != nil || sup != nil {
			base = p.bounded(composeMathScripts(base, sub, sup))
		}
		if base == nil || len(parts) >= 128 {
			return nil
		}
		parts = append(parts, base)
	}
	if group {
		return nil
	}
	return p.bounded(joinMathMasks(parts))
}

// Bound intermediate images as well as the final image, so deeply nested
// expressions cannot allocate an unbounded collection of masks.
func (p *complexMathParser) bounded(img *mathImage) *mathImage {
	if img == nil || img.mask == nil {
		return nil
	}
	n := img.mask.Bounds().Dx() * img.mask.Bounds().Dy()
	if n > maxTranscriptMathPixels-p.pixels {
		return nil
	}
	p.pixels += n
	return img
}

func (p *complexMathParser) argument(size float32, depth int) *mathImage {
	if p.pos >= len(p.source) {
		return nil
	}
	if p.source[p.pos] == '{' {
		p.pos++
		return p.expression(size, depth, true)
	}
	return p.atom(size, depth)
}

func (p *complexMathParser) atom(size float32, depth int) *mathImage {
	if p.pos >= len(p.source) || depth > 16 || size < 6 || p.tokens >= 128 {
		return nil
	}
	p.tokens++
	start := p.pos
	c := p.source[p.pos]
	if c == '{' {
		p.pos++
		return p.expression(size, depth+1, true)
	}
	if c == '\\' {
		p.pos++
		for p.pos < len(p.source) && (p.source[p.pos] >= 'a' && p.source[p.pos] <= 'z' || p.source[p.pos] >= 'A' && p.source[p.pos] <= 'Z') {
			p.pos++
		}
		if p.pos == start+1 {
			return nil
		}
		command := p.source[start:p.pos]
		switch command {
		case `\frac`, `\dfrac`, `\tfrac`:
			num := p.argument(size*.85, depth+1)
			den := p.argument(size*.85, depth+1)
			if num == nil || den == nil {
				return nil
			}
			return composeMathFraction(num, den)
		case `\sqrt`:
			// Indexed roots need distinct layout; never render their index as
			// the radicand and silently change the meaning.
			if p.pos < len(p.source) && p.source[p.pos] == '[' {
				return nil
			}
			arg := p.argument(size, depth+1)
			if arg == nil {
				return nil
			}
			return composeMathRoot(arg, size)
		case `\begin`:
			return p.matrix(size, depth+1)
		default:
			return p.math.render(command, size)
		}
	}
	if !mathASCIIAtom(c) && !strings.ContainsRune("+-=*/(),.[]|<>!", rune(c)) {
		return nil
	}
	p.pos++
	return p.math.render(p.source[start:p.pos], size)
}

func mathMaskImage(mask *image.NRGBA) *mathImage {
	if mask == nil || mask.Bounds().Dx() <= 0 || mask.Bounds().Dy() <= 0 || mask.Bounds().Dx() > 2048 || mask.Bounds().Dy() > 512 {
		return nil
	}
	return &mathImage{bitmap: ui.NewBitmap(mask), mask: mask, w: float32(mask.Bounds().Dx()) / 2, h: float32(mask.Bounds().Dy()) / 2}
}

func joinMathMasks(parts []*mathImage) *mathImage {
	if len(parts) == 0 {
		return nil
	}
	w, h := 0, 0
	for _, part := range parts {
		if part == nil || part.mask == nil {
			return nil
		}
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
	return mathMaskImage(mask)
}

func composeMathScripts(base, sub, sup *mathImage) *mathImage {
	if base == nil || base.mask == nil {
		return nil
	}
	b := base.mask.Bounds()
	top, bottom, sw := 0, 0, 0
	if sup != nil {
		top = sup.mask.Bounds().Dy() / 2
		sw = sup.mask.Bounds().Dx()
	}
	if sub != nil {
		bottom = sub.mask.Bounds().Dy() / 2
		sw = max(sw, sub.mask.Bounds().Dx())
	}
	w, h := b.Dx()+sw, top+b.Dy()+bottom
	if w > 2048 || h > 512 {
		return nil
	}
	mask := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(mask, image.Rect(0, top, b.Dx(), top+b.Dy()), base.mask, b.Min, draw.Over)
	if sup != nil {
		s := sup.mask.Bounds()
		draw.Draw(mask, image.Rect(b.Dx(), 0, b.Dx()+s.Dx(), s.Dy()), sup.mask, s.Min, draw.Over)
	}
	if sub != nil {
		s := sub.mask.Bounds()
		draw.Draw(mask, image.Rect(b.Dx(), h-s.Dy(), b.Dx()+s.Dx(), h), sub.mask, s.Min, draw.Over)
	}
	return mathMaskImage(mask)
}

func composeMathFraction(num, den *mathImage) *mathImage {
	n, d := num.mask.Bounds(), den.mask.Bounds()
	w, h := max(n.Dx(), d.Dx())+8, n.Dy()+d.Dy()+8
	if w > 2048 || h > 512 {
		return nil
	}
	mask := image.NewNRGBA(image.Rect(0, 0, w, h))
	nx, dx := (w-n.Dx())/2, (w-d.Dx())/2
	draw.Draw(mask, image.Rect(nx, 0, nx+n.Dx(), n.Dy()), num.mask, n.Min, draw.Over)
	draw.Draw(mask, image.Rect(dx, n.Dy()+8, dx+d.Dx(), h), den.mask, d.Min, draw.Over)
	draw.Draw(mask, image.Rect(2, n.Dy()+3, w-2, n.Dy()+5), image.NewUniform(color.NRGBA{R: 255, G: 255, B: 255, A: 255}), image.Point{}, draw.Src)
	return mathMaskImage(mask)
}

func composeMathRoot(arg *mathImage, size float32) *mathImage {
	b := arg.mask.Bounds()
	pad := max(8, int(size*.7))
	w, h := b.Dx()+pad, b.Dy()+6
	if w > 2048 || h > 512 {
		return nil
	}
	mask := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(mask, image.Rect(pad, 6, w, h), arg.mask, b.Min, draw.Over)
	r := vector.NewRasterizer(w, h)
	r.MoveTo(0, float32(h)*.55)
	r.LineTo(float32(pad)*.35, float32(h)*.9)
	r.LineTo(float32(pad)*.75, 2)
	r.LineTo(float32(w), 2)
	r.LineTo(float32(w), 4)
	r.LineTo(float32(pad)*.85, 4)
	r.LineTo(float32(pad)*.43, float32(h))
	r.ClosePath()
	r.Draw(mask, mask.Bounds(), image.NewUniform(color.White), image.Point{})
	return mathMaskImage(mask)
}

// matrix accepts a bounded rectangular matrix, not arbitrary TeX environments.
func (p *complexMathParser) matrix(size float32, depth int) *mathImage {
	if depth > 16 || p.pos >= len(p.source) || p.source[p.pos] != '{' {
		return nil
	}
	endName := strings.IndexByte(p.source[p.pos+1:], '}')
	if endName < 0 {
		return nil
	}
	endName += p.pos + 1
	name := p.source[p.pos+1 : endName]
	switch name {
	case "matrix", "pmatrix", "bmatrix", "vmatrix":
	default:
		return nil
	}
	p.pos = endName + 1
	closing := `\end{` + name + `}`
	endBody := strings.Index(p.source[p.pos:], closing)
	if endBody < 0 {
		return nil
	}
	endBody += p.pos
	body := p.source[p.pos:endBody]
	if strings.Contains(body, `\begin`) || strings.Contains(body, `\end`) {
		return nil
	}
	rows := [][]*mathImage{{}}
	start, nested := 0, 0
	appendCell := func(end int) bool {
		cell := strings.TrimSpace(body[start:end])
		if cell == "" || len(rows[len(rows)-1]) >= 6 {
			return false
		}
		img := p.math.render(cell, size*.85)
		if img == nil {
			return false
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], img)
		return true
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{':
			nested++
		case '}':
			nested--
			if nested < 0 {
				return nil
			}
		case '&':
			if nested == 0 {
				if !appendCell(i) {
					return nil
				}
				start = i + 1
			}
		case '\\':
			if nested == 0 && i+1 < len(body) && body[i+1] == '\\' {
				if !appendCell(i) || len(rows) >= 6 {
					return nil
				}
				rows = append(rows, nil)
				i++
				start = i + 1
			}
		}
	}
	if nested != 0 || !appendCell(len(body)) {
		return nil
	}
	cols := len(rows[0])
	for _, row := range rows {
		if len(row) != cols {
			return nil
		}
	}
	p.pos = endBody + len(closing)
	return composeMathMatrix(rows, name)
}

func composeMathMatrix(rows [][]*mathImage, name string) *mathImage {
	colWidths, rowHeights := make([]int, len(rows[0])), make([]int, len(rows))
	for i, row := range rows {
		for j, cell := range row {
			colWidths[j] = max(colWidths[j], cell.mask.Bounds().Dx())
			rowHeights[i] = max(rowHeights[i], cell.mask.Bounds().Dy())
		}
	}
	const gap = 8
	w, h := gap*(len(colWidths)-1), gap*(len(rowHeights)-1)
	for _, n := range colWidths {
		w += n
	}
	for _, n := range rowHeights {
		h += n
	}
	pad := 0
	if name != "matrix" {
		pad = 10
	}
	w += 2 * pad
	if w <= 0 || h <= 0 || w > 2048 || h > 512 {
		return nil
	}
	mask := image.NewNRGBA(image.Rect(0, 0, w, h))
	y := 0
	for i, row := range rows {
		x := pad
		for j, cell := range row {
			b := cell.mask.Bounds()
			dx, dy := x+(colWidths[j]-b.Dx())/2, y+(rowHeights[i]-b.Dy())/2
			draw.Draw(mask, image.Rect(dx, dy, dx+b.Dx(), dy+b.Dy()), cell.mask, b.Min, draw.Over)
			x += colWidths[j] + gap
		}
		y += rowHeights[i] + gap
	}
	if pad != 0 {
		white := image.NewUniform(color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		if name == "pmatrix" {
			r := vector.NewRasterizer(w, h)
			fh, fw := float32(h), float32(w)
			r.MoveTo(8, 0)
			r.QuadTo(0, fh/2, 8, fh)
			r.LineTo(6, fh)
			r.QuadTo(-2, fh/2, 6, 0)
			r.ClosePath()
			r.MoveTo(fw-8, 0)
			r.QuadTo(fw, fh/2, fw-8, fh)
			r.LineTo(fw-6, fh)
			r.QuadTo(fw+2, fh/2, fw-6, 0)
			r.ClosePath()
			r.Draw(mask, mask.Bounds(), white, image.Point{})
		} else {
			draw.Draw(mask, image.Rect(3, 0, 5, h), white, image.Point{}, draw.Over)
			draw.Draw(mask, image.Rect(w-5, 0, w-3, h), white, image.Point{}, draw.Over)
		}
		if name == "bmatrix" {
			draw.Draw(mask, image.Rect(3, 0, pad-2, 2), white, image.Point{}, draw.Over)
			draw.Draw(mask, image.Rect(3, h-2, pad-2, h), white, image.Point{}, draw.Over)
			draw.Draw(mask, image.Rect(w-pad+2, 0, w-3, 2), white, image.Point{}, draw.Over)
			draw.Draw(mask, image.Rect(w-pad+2, h-2, w-3, h), white, image.Point{}, draw.Over)
		}
	}
	return mathMaskImage(mask)
}
