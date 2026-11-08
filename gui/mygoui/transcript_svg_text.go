package mygoui

import (
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// This deliberately narrow extension supports simple labels on top of basic
// SVG shapes. Anything needing SVG text layout falls back to source rather
// than silently dropping labels or reordering drawing.
type svgTextLabel struct {
	text       string
	x, y, size float32
	color      ui.Color
	anchor     string
	font       string
	weight     int
	italic     bool
}

func parseLabeledTranscriptSVG(source string) (*ui.SVG, []svgTextLabel, [4]float32) {
	var view [4]float32
	if len(source) == 0 || len(source) > maxTranscriptSVGBytes {
		return nil, nil, view
	}
	d := xml.NewDecoder(strings.NewReader(source))
	depth := 0
	rootSeen, textSeen, inText := false, false, false
	type ancestor struct {
		name        string
		safe        bool
		scale, x, y float64
	}
	var stack []ancestor
	var labels []svgTextLabel
	var ranges [][2]int
	var start int
	var current svgTextLabel
	for {
		before := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, view
		}
		switch v := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 || inText {
				return nil, nil, view
			}
			if depth == 1 {
				if rootSeen || v.Name.Local != "svg" {
					return nil, nil, view
				}
				rootSeen = true
				found := false
				for _, a := range v.Attr {
					switch a.Name.Local {
					case "viewBox":
						parts := strings.Fields(strings.ReplaceAll(a.Value, ",", " "))
						if len(parts) != 4 {
							return nil, nil, view
						}
						for i, part := range parts {
							n, err := strconv.ParseFloat(part, 32)
							if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000 {
								return nil, nil, view
							}
							view[i] = float32(n)
						}
						found = true
					case "xmlns", "width", "height", "version":
					default:
						return nil, nil, view
					}
				}
				if !found || view[2] <= 0 || view[3] <= 0 {
					return nil, nil, view
				}
				stack = append(stack, ancestor{name: "svg", safe: true, scale: 1})
				continue
			}
			parent := stack[len(stack)-1]
			next := ancestor{name: v.Name.Local}
			if v.Name.Local == "g" {
				local, ok := parseSVGGroupTransform(v.Attr)
				next.safe = ok && parent.safe
				if next.safe {
					next.scale = parent.scale * local.scale
					next.x = parent.x + parent.scale*local.x
					next.y = parent.y + parent.scale*local.y
					if next.scale < .0001 || next.scale > 10000 || math.Abs(next.x) > 1e7 || math.Abs(next.y) > 1e7 {
						return nil, nil, view
					}
				}
			}
			if v.Name.Local == "text" && (parent.name == "svg" || parent.name == "g" && parent.safe) {
				if len(labels) >= 32 || (v.Name.Space != "" && v.Name.Space != "http://www.w3.org/2000/svg") {
					return nil, nil, view
				}
				textSeen, inText, start = true, true, before
				current = svgTextLabel{size: 16, color: ui.RGB(0, 0, 0)}
				if !parseSVGTextAttrs(v.Attr, &current) {
					return nil, nil, view
				}
				current.x = float32(parent.x + parent.scale*float64(current.x))
				current.y = float32(parent.y + parent.scale*float64(current.y))
				current.size *= float32(parent.scale)
			} else if textSeen && (v.Name.Local != "g" || !next.safe) {
				return nil, nil, view
			}
			stack = append(stack, next)
		case xml.CharData:
			if inText {
				current.text += string(v)
				if len(current.text) > 512 || strings.ContainsAny(current.text, "\n\r\t") {
					return nil, nil, view
				}
			}
		case xml.EndElement:
			if inText && v.Name.Local == "text" {
				if current.text == "" {
					return nil, nil, view
				}
				labels = append(labels, current)
				ranges = append(ranges, [2]int{start, int(d.InputOffset())})
				inText = false
			}
			depth--
			stack = stack[:len(stack)-1]
		case xml.Directive:
			return nil, nil, view
		}
	}
	if depth != 0 || len(labels) == 0 {
		return nil, nil, view
	}
	var shapes strings.Builder
	pos := 0
	for _, r := range ranges {
		shapes.WriteString(source[pos:r[0]])
		pos = r[1]
	}
	shapes.WriteString(source[pos:])
	img, err := parseTranscriptSVG(shapes.String())
	if err != nil {
		return nil, nil, view
	}
	w, h := img.Size()
	if math.Abs(float64(w/h-view[2]/view[3])) > .001 {
		return nil, nil, view
	}
	scale := min(float32(1), 640/w, 320/h) * w / view[2]
	for _, label := range labels {
		if !isFiniteSVGSize(label.size*scale) || label.size*scale > 1024 {
			return nil, nil, view
		}
	}
	return img, labels, view
}

func isFiniteSVGSize(n float32) bool {
	return n > 0 && !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0)
}

type svgGroupTransform struct{ scale, x, y float64 }

// Only translations and positive uniform scales can be applied to text by
// this native overlay. Other transforms must fall back to the original SVG.
func parseSVGGroupTransform(attrs []xml.Attr) (svgGroupTransform, bool) {
	t := svgGroupTransform{scale: 1}
	if len(attrs) == 0 {
		return t, true
	}
	if len(attrs) != 1 || attrs[0].Name.Space != "" || attrs[0].Name.Local != "transform" {
		return t, false
	}
	s := strings.TrimSpace(attrs[0].Value)
	if len(s) == 0 || len(s) > 256 {
		return t, false
	}
	for s != "" {
		open := strings.IndexByte(s, '(')
		close := strings.IndexByte(s, ')')
		if open < 0 || close < open {
			return t, false
		}
		name := strings.TrimSpace(s[:open])
		args := s[open+1 : close]
		if strings.Count(args, ",") > 1 {
			return t, false
		}
		if before, after, hasComma := strings.Cut(args, ","); hasComma && (len(strings.Fields(before)) != 1 || len(strings.Fields(after)) != 1) {
			return t, false
		}
		parts := strings.Fields(strings.ReplaceAll(args, ",", " "))
		if len(parts) < 1 || len(parts) > 2 {
			return t, false
		}
		values := [2]float64{}
		for i, part := range parts {
			n, err := strconv.ParseFloat(part, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000 {
				return t, false
			}
			values[i] = n
		}
		switch name {
		case "translate":
			t.x += t.scale * values[0]
			t.y += t.scale * values[1]
		case "scale":
			if values[0] <= 0 || len(parts) == 2 && values[0] != values[1] {
				return t, false
			}
			t.scale *= values[0]
		default:
			return t, false
		}
		if t.scale < .0001 || t.scale > 10000 || math.Abs(t.x) > 1e7 || math.Abs(t.y) > 1e7 {
			return t, false
		}
		s = strings.TrimSpace(s[close+1:])
	}
	return t, true
}

func parseSVGTextAttrs(attrs []xml.Attr, label *svgTextLabel) bool {
	hasX, hasY := false, false
	for _, a := range attrs {
		if a.Name.Space != "" {
			return false
		}
		switch a.Name.Local {
		case "x", "y", "font-size":
			n, err := strconv.ParseFloat(strings.TrimSuffix(a.Value, "px"), 32)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000 {
				return false
			}
			switch a.Name.Local {
			case "x":
				label.x, hasX = float32(n), true
			case "y":
				label.y, hasY = float32(n), true
			case "font-size":
				label.size = float32(n)
			}
		case "text-anchor":
			if a.Value != "start" && a.Value != "middle" && a.Value != "end" {
				return false
			}
			label.anchor = a.Value
		case "fill":
			color, ok := svgTextColor(a.Value)
			if !ok {
				return false
			}
			label.color = color
		case "font-family":
			switch a.Value {
			case "sans-serif", "serif", "monospace", "system-ui":
				label.font = a.Value
			default:
				return false
			}
		case "font-weight":
			switch a.Value {
			case "normal", "400":
				label.weight = 400
			case "bold", "700":
				label.weight = 700
			default:
				return false
			}
		case "font-style":
			if a.Value != "normal" && a.Value != "italic" {
				return false
			}
			label.italic = a.Value == "italic"
		default:
			return false
		}
	}
	return hasX && hasY && label.size > 0 && label.size <= 1024
}

func svgTextColor(value string) (ui.Color, bool) {
	switch value {
	case "black":
		return ui.RGB(0, 0, 0), true
	case "white":
		return ui.RGB(255, 255, 255), true
	case "red":
		return ui.RGB(255, 0, 0), true
	}
	if len(value) == 4 && value[0] == '#' {
		value = "#" + strings.Repeat(value[1:2], 2) + strings.Repeat(value[2:3], 2) + strings.Repeat(value[3:4], 2)
	}
	if len(value) != 7 || value[0] != '#' {
		return ui.Color{}, false
	}
	n, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return ui.Color{}, false
	}
	return ui.RGB(uint8(n>>16), uint8(n>>8), uint8(n)), true
}

func drawLabeledTranscriptSVG(p *ui.Painter, rect ui.Rect, img *ui.SVG, labels []svgTextLabel, view [4]float32) {
	p.Image(img, rect, ui.Contain)
	scale := rect.W / view[2]
	for _, label := range labels {
		size := label.size * scale
		x := rect.X + (label.x-view[0])*scale
		y := rect.Y + (label.y-view[1])*scale - size*.8
		span := ui.Span{Text: label.text, Size: size, Color: label.color, Font: label.font, Weight: label.weight, Italic: label.italic}
		if label.anchor != "" && label.anchor != "start" {
			width, _ := p.MeasureText(0, span)
			if label.anchor == "middle" {
				x -= width / 2
			} else {
				x -= width
			}
		}
		p.RichText(x, y, 0, span)
	}
}
