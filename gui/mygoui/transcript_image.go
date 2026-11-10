package mygoui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"github.com/getan/golder/gui/appsession"
)

const (
	maxTranscriptImageBytes  = 8 << 20
	maxTranscriptImagePixels = 12_000_000
	maxTranscriptImageSide   = 8192
	maxTranscriptImages      = 16
)

type transcriptMarkdownKey struct{}
type transcriptMarkdownState struct {
	project string
	options *markdown.Options
}

func transcriptMarkdownOptions(project string) *markdown.Options {
	var loads atomic.Int32
	math := newTranscriptMath()
	return &markdown.Options{
		Extensions:     parser.Default | parser.ExtDollarMath,
		ShareSelection: true,
		InlineMath:     math.inline,
		Math:           math.block,
		Code: map[string]func(*ui.Context, *markdown.CodeBlockView){
			"svg": renderSVGCodeBlock, "SVG": renderSVGCodeBlock,
			"xml": renderSVGCodeBlock, "XML": renderSVGCodeBlock,
			"mermaid": renderMermaidCodeBlock,
		},
		LoadImage: func(dest string) (ui.ImageSource, error) {
			if loads.Add(1) > maxTranscriptImages {
				return nil, errors.New("too many images in message")
			}
			return loadTranscriptImage(project, dest)
		},
	}
}

// loadTranscriptImage does not fetch URLs or read paths outside project.
func loadTranscriptImage(project, dest string) (ui.ImageSource, error) {
	invalid := errors.New("unsupported or unsafe transcript image")
	var data []byte
	var mime string
	isSVG := false
	if strings.HasPrefix(dest, "data:") {
		header, encoded, ok := strings.Cut(dest, ",")
		if !ok || len(encoded) > base64.StdEncoding.EncodedLen(maxTranscriptImageBytes) {
			return nil, invalid
		}
		mime = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if header != "data:"+mime+";base64" || !transcriptImageMIME(mime) {
			return nil, invalid
		}
		var err error
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) > maxTranscriptImageBytes {
			return nil, invalid
		}
		isSVG = mime == "image/svg+xml"
	} else {
		if strings.ContainsAny(dest, "\x00\\") && !filepath.IsAbs(dest) {
			return nil, invalid
		}
		path := dest
		if !filepath.IsAbs(dest) {
			u, err := url.Parse(dest)
			if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasPrefix(dest, "//") {
				return nil, invalid
			}
			path = u.Path
		}
		isSVG = strings.EqualFold(filepath.Ext(path), ".svg")
		var err error
		data, err = appsession.ReadTranscriptImage(project, path, maxTranscriptImageBytes)
		if err != nil {
			return nil, err
		}
	}
	if isSVG {
		return renderRichPreview(context.Background(), string(data), false)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > maxTranscriptImageSide || config.Height > maxTranscriptImageSide || int64(config.Width)*int64(config.Height) > maxTranscriptImagePixels {
		return nil, invalid
	}
	if !transcriptImageFormat(format) || mime != "" && mime != "image/"+format && !(mime == "image/jpg" && format == "jpeg") {
		return nil, invalid
	}
	return ui.DecodeBitmap(data)
}

func transcriptImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/bmp", "image/svg+xml":
		return true
	}
	return false
}

func transcriptImageFormat(format string) bool {
	switch format {
	case "png", "jpeg", "gif", "webp", "bmp":
		return true
	}
	return false
}
