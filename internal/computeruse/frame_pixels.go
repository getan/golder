package computeruse

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image/png"
)

// framePixelDigest identifies decoded pixels, not the PNG encoding. Two
// encodings of the same image compare equal; a changed pixel, geometry or
// undecodable frame still changes or rejects the digest.
func framePixelDigest(pngBytes []byte) ([32]byte, error) {
	var zero [32]byte
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
	hash := sha256.New()
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	_, _ = hash.Write(header[:])
	row := make([]byte, 0, width*8)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row = row[:0]
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			row = append(row, byte(r>>8), byte(r), byte(g>>8), byte(g), byte(b>>8), byte(b), byte(a>>8), byte(a))
		}
		_, _ = hash.Write(row)
	}
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out, nil
}
