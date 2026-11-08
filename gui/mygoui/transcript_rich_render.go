package mygoui

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"

	"github.com/getan/golder/gui/mermaidwasm"
	"github.com/getan/golder/gui/svgwasm"
)

const maxRendererModuleBytes = 4 << 20

var richRenderSlots = make(chan struct{}, 2)

type richPreviewState struct {
	mu         sync.Mutex
	source     string
	generation uint64
	running    bool
	done       bool
	bitmap     *ui.Bitmap
}

// preview starts a worker only for a finished code block. It leaves the source
// visible and never allows a stale worker result to replace a newer block.
func (s *richPreviewState) preview(c *ui.Context, source string, mermaid bool) (*ui.Bitmap, bool) {
	s.mu.Lock()
	if s.source != source {
		s.source = source
		s.generation++
		s.running, s.done, s.bitmap = false, false, nil
	}
	if len(source) == 0 || len(source) > 512<<10 || mermaid && len(source) > 32<<10 {
		s.done = true
	}
	if !s.done && !s.running {
		s.running = true
		generation := s.generation
		services := c.Services()
		go func() {
			bitmap, _ := renderRichPreview(context.Background(), source, mermaid)
			s.mu.Lock()
			if s.generation == generation {
				s.bitmap, s.running, s.done = bitmap, false, true
			}
			s.mu.Unlock()
			services.Invalidate()
		}()
	}
	bitmap, loading := s.bitmap, s.running
	s.mu.Unlock()
	return bitmap, loading
}

func renderRichPreview(ctx context.Context, source string, mermaid bool) (*ui.Bitmap, error) {
	return withRichRenderSlot(ctx, func(ctx context.Context) (*ui.Bitmap, error) {
		resources, err := mygo.App.Path(mygo.PathResources)
		if err != nil || resources == "" {
			return nil, errors.New("renderer resources unavailable")
		}
		svgModule, err := readRendererModule(filepath.Join(resources, "renderers", "svg.wasm.gz"))
		if err != nil {
			return nil, err
		}
		var mermaidModule []byte
		if mermaid {
			mermaidModule, err = readRendererModule(filepath.Join(resources, "renderers", "mermaid.wasm.gz"))
			if err != nil {
				return nil, err
			}
		}
		return renderRichCore(ctx, source, mermaid, svgModule, mermaidModule)
	})
}

func readRendererModule(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRendererModuleBytes {
		return nil, errors.New("renderer module unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("renderer module changed")
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, errors.New("invalid compressed renderer module")
	}
	defer zr.Close()
	data, err := io.ReadAll(io.LimitReader(zr, maxRendererModuleBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxRendererModuleBytes {
		return nil, errors.New("invalid renderer module")
	}
	return data, nil
}

func renderRichWithModules(ctx context.Context, source string, mermaid bool, svgModule, mermaidModule []byte) (*ui.Bitmap, error) {
	return withRichRenderSlot(ctx, func(ctx context.Context) (*ui.Bitmap, error) {
		return renderRichCore(ctx, source, mermaid, svgModule, mermaidModule)
	})
}

func withRichRenderSlot(ctx context.Context, fn func(context.Context) (*ui.Bitmap, error)) (*ui.Bitmap, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	select {
	case richRenderSlots <- struct{}{}:
		defer func() { <-richRenderSlots }()
		return fn(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func renderRichCore(ctx context.Context, source string, mermaid bool, svgModule, mermaidModule []byte) (*ui.Bitmap, error) {
	if len(source) == 0 || len(source) > 512<<10 || mermaid && len(source) > 32<<10 {
		return nil, errors.New("diagram exceeds size limit")
	}
	fontText := source
	if !mermaid {
		if err := svgwasm.Validate([]byte(source)); err != nil {
			return nil, err
		}
		var err error
		fontText, err = svgLabelText(source)
		if err != nil {
			return nil, err
		}
	}
	font, err := fontForText(fontText)
	if err != nil {
		return nil, err
	}
	svg := source
	if mermaid {
		svg, err = mermaidwasm.RenderWithFont(ctx, mermaidModule, source, font)
		if err != nil {
			return nil, err
		}
	}
	if err = svgwasm.Validate([]byte(svg)); err != nil {
		return nil, err
	}
	// Validate the actual labels too: the renderer must not silently replace a
	// missing glyph with an empty box, including for generated Mermaid SVG.
	if !fontSupportsSVG(font, svg) {
		return nil, errors.New("installed font does not cover SVG labels")
	}
	imageData, err := svgwasm.Render(ctx, svgModule, []byte(svg), font)
	if err != nil {
		return nil, err
	}
	config, err := png.DecodeConfig(bytes.NewReader(imageData))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 1280 || config.Height > 640 {
		return nil, errors.New("invalid or oversized renderer image")
	}
	return ui.DecodeBitmap(imageData)
}

func fontForText(text string) ([]byte, error) {
	runes := textRunes(text)
	if fontSupports(goregular.TTF, runes) {
		return goregular.TTF, nil
	}
	for _, path := range systemFontPaths() {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 32<<20 {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		f.Close()
		if err == nil && len(data) <= 32<<20 && fontSupports(data, runes) {
			return data, nil
		}
	}
	return nil, errors.New("no installed font covers the diagram")
}

func systemFontPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/System/Library/Fonts/PingFang.ttc", "/System/Library/Fonts/Hiragino Sans GB.ttc"}
	case "windows":
		root := os.Getenv("WINDIR")
		if root == "" {
			return nil
		}
		return []string{filepath.Join(root, "Fonts", "msyh.ttc"), filepath.Join(root, "Fonts", "simsun.ttc")}
	case "linux":
		return []string{"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc", "/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc"}
	}
	return nil
}

func textRunes(text string) map[rune]struct{} {
	runes := make(map[rune]struct{})
	for _, r := range text {
		if !unicode.IsSpace(r) && !unicode.IsControl(r) {
			runes[r] = struct{}{}
			if len(runes) > 4096 {
				return nil
			}
		}
	}
	return runes
}

func fontSupports(data []byte, runes map[rune]struct{}) bool {
	if runes == nil {
		return false
	}
	collection, err := sfnt.ParseCollection(data)
	if err != nil {
		return false
	}
	for i := 0; i < min(collection.NumFonts(), 16); i++ {
		font, err := collection.Font(i)
		if err != nil {
			continue
		}
		var buffer sfnt.Buffer
		matched := true
		for r := range runes {
			glyph, err := font.GlyphIndex(&buffer, r)
			if err != nil || glyph == 0 {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func fontSupportsSVG(font []byte, svg string) bool {
	text, err := svgLabelText(svg)
	return err == nil && fontSupports(font, textRunes(text))
}

func svgLabelText(svg string) (string, error) {
	d := xml.NewDecoder(strings.NewReader(svg))
	depth := 0
	var text strings.Builder
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch v := token.(type) {
		case xml.StartElement:
			if v.Name.Local == "text" || depth > 0 {
				depth++
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
		case xml.CharData:
			if depth > 0 {
				if text.Len()+len(v) > 64<<10 {
					return "", errors.New("SVG labels exceed limit")
				}
				text.Write(v)
			}
		}
	}
	if depth != 0 {
		return "", errors.New("invalid SVG text")
	}
	return text.String(), nil
}

func drawRichBitmap(c *ui.Context, bitmap *ui.Bitmap, label string) {
	w, h := bitmap.Size()
	if w <= 0 || h <= 0 {
		return
	}
	scale := min(float32(1), 640/float32(w), 320/float32(h))
	ui.Column(c).Padding(10).Radius(7).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Image(c, bitmap).Width(float32(w) * scale).MaxWidthPercent(100).AspectRatio(float32(w) / float32(h)).Label(fmt.Sprintf("%s image", label))
	})
}
