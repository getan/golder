package docxmcp

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	"github.com/mmonterroca/docxgo/v2/domain"
)

type loadedImage struct {
	data          []byte
	format        domain.ImageFormat
	width, height int
}

// loadImage decodes the image fully and re-encodes it as PNG. Re-encoding drops
// metadata and any trailing payload; the pixel bound is checked before decode.
func (p *Plugin) loadImage(rel string) (*loadedImage, error) {
	_, data, err := p.ws.ReadImage(rel)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errf(codeImageInvalid, "cannot decode image header")
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, errf(codeImageTooLarge, "image exceeds the %d megapixel limit", maxImagePixels/1_000_000)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errf(codeImageInvalid, "cannot decode image")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, errf(codeImageInvalid, "cannot re-encode image")
	}
	if out.Len() > maxImageBytes {
		return nil, errf(codeImageTooLarge, "re-encoded image exceeds the %d MiB limit", maxImageBytes>>20)
	}
	return &loadedImage{data: out.Bytes(), format: domain.ImageFormatPNG, width: cfg.Width, height: cfg.Height}, nil
}
