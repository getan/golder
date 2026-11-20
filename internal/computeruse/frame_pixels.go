package computeruse

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash"
	"image/png"
)

const tileCount = 4 // per axis

type frameFingerprint struct {
	tiles []byte
}

func fingerprintFrame(pngBytes []byte) (frameFingerprint, error) {
	var zero frameFingerprint
	if len(pngBytes) > MaxFrameBytes {
		return zero, errors.New("frame exceeds byte limit")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || cfg.Height <= 0 || cfg.Width <= 0 || cfg.Width > MaxFramePixels/cfg.Height {
		return zero, errors.New("frame dimensions are invalid or exceed the pixel limit")
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return zero, err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var tiles [tileCount * tileCount]hash.Hash
	for i := range tiles {
		tiles[i] = sha256.New()
	}
	row := make([]byte, 0, width*8)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row = row[:0]
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			row = append(row, byte(r>>8), byte(r), byte(g>>8), byte(g), byte(b>>8), byte(b), byte(a>>8), byte(a))
		}
		for col := 0; col < tileCount; col++ {
			// Use ceil at the boundaries: pixel x belongs to floor(x*4/width).
			left, right := (col*width+tileCount-1)/tileCount, ((col+1)*width+tileCount-1)/tileCount
			tiles[y*tileCount/height*tileCount+col].Write(row[8*left : 8*right])
		}
	}
	var out frameFingerprint
	for _, tile := range tiles {
		d := tile.Sum(nil)
		// Regional equality supplements, but never replaces, native AX checks.
		out.tiles = append(out.tiles, d...)
	}
	return out, nil
}
