package mygoui

import (
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// MyGo's SVG renderer intentionally supports only a subset of SVG. Never show
// a partial diagram as if it were complete: unsupported content stays code.
const maxTranscriptSVGBytes = 64 << 10
const maxTranscriptSVGElements = 1024

type svgPreviewState struct {
	source string
	image  *ui.SVG
}

type svgPreviewKey struct{}

func renderSVGCodeBlock(c *ui.Context, b *markdown.CodeBlockView) {
	// Incomplete streaming XML is shown as code until its fence closes.
	if b.Open {
		markdown.DefaultCodeBlock(c, b)
		return
	}
	if strings.EqualFold(b.Language, "xml") && !strings.HasPrefix(strings.TrimSpace(b.Code()), "<svg") {
		markdown.DefaultCodeBlock(c, b)
		return
	}
	box := ui.Column(c).AlignSelf(ui.Stretch).Gap(6)
	state := *ui.Local(box, svgPreviewKey{}, func() *svgPreviewState { return &svgPreviewState{} })
	source := b.Code()
	if state.source != source {
		state.source = source
		state.image, _ = parseTranscriptSVG(source)
	}
	box.Children(func() {
		if state.image != nil {
			w, h := state.image.Size()
			scale := min(float32(1), 640/w, 320/h)
			ui.Column(c).Padding(10).Radius(7).Background(c.Theme().Surface).Children(func() {
				ui.Text(c, "SVG preview").FontSize(11).TextColor(c.Theme().TextMuted)
				ui.Image(c, state.image).Width(w * scale).MaxWidthPercent(100).AspectRatio(w / h).Label("SVG image preview")
			})
		} else {
			ui.Text(c, "SVG preview unavailable; showing source").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		// Always keep the original, selectable and copyable source visible.
		markdown.DefaultCodeBlock(c, b)
	})
}

// parseTranscriptSVG accepts a bounded, self-contained subset of SVG, with no
// external resources, scripting, text or features the native renderer omits.
func parseTranscriptSVG(source string) (*ui.SVG, error) {
	invalid := errors.New("unsupported or invalid SVG preview")
	if len(source) == 0 || len(source) > maxTranscriptSVGBytes {
		return nil, invalid
	}
	d := xml.NewDecoder(strings.NewReader(source))
	depth, count, roots := 0, 0, 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, invalid
		}
		switch v := tok.(type) {
		case xml.StartElement:
			depth++
			count++
			if depth > 32 || count > maxTranscriptSVGElements || !previewSVGElement(v.Name.Local) {
				return nil, invalid
			}
			if depth == 1 {
				roots++
				if roots != 1 || v.Name.Local != "svg" {
					return nil, invalid
				}
			} else if v.Name.Local == "svg" {
				return nil, invalid
			}
			if v.Name.Space != "" && v.Name.Space != "http://www.w3.org/2000/svg" {
				return nil, invalid
			}
			for _, a := range v.Attr {
				if !previewSVGAttribute(a) {
					return nil, invalid
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return nil, invalid
			}
		case xml.Comment:
		case xml.ProcInst:
			if v.Target != "xml" || roots != 0 {
				return nil, invalid
			}
		default: // In particular, never accept a DTD or entity declarations.
			return nil, invalid
		}
	}
	if depth != 0 || roots != 1 {
		return nil, invalid
	}
	image, err := ui.ParseSVG([]byte(source))
	if err != nil {
		return nil, invalid
	}
	w, h := image.Size()
	if w <= 0 || h <= 0 || math.IsNaN(float64(w)) || math.IsNaN(float64(h)) || math.IsInf(float64(w), 0) || math.IsInf(float64(h), 0) {
		return nil, invalid
	}
	return image, nil
}

func previewSVGElement(name string) bool {
	switch name {
	case "svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
		return true
	}
	return false
}

func previewSVGAttribute(a xml.Attr) bool {
	if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
		return a.Value == "http://www.w3.org/2000/svg"
	}
	if a.Name.Space != "" || strings.Contains(strings.ToLower(a.Value), "url(") {
		return false
	}
	switch a.Name.Local {
	case "version", "viewBox", "width", "height", "x", "y", "x1", "x2", "y1", "y2", "cx", "cy", "r", "rx", "ry", "d", "points", "transform", "fill", "fill-rule", "fill-opacity", "stroke", "stroke-width", "stroke-opacity", "stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "stroke-dasharray", "stroke-dashoffset", "opacity":
		return true
	}
	return false
}
