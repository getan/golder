package computeruse

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	_ "image/png"
)

// framePixelDigest identifies decoded pixels, not the PNG encoding. Two
// encodings of the same image compare equal; a changed pixel, geometry or
// undecodable frame still changes or rejects the digest.
func framePixelDigest(pngBytes []byte) ([32]byte, error) {
	var zero [32]byte
	img, _, err := image.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return zero, err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || int64(width)*int64(height) > MaxFramePixels {
		return zero, errors.New("frame dimensions are invalid or exceed the pixel limit")
	}
	hash := sha256.New()
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	_, _ = hash.Write(header[:])
	row := make([]byte, 0, width*4)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row = row[:0]
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			row = append(row, byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8))
		}
		_, _ = hash.Write(row)
	}
	var out [32]byte
	copy(out[:], hash.Sum(nil))
	return out, nil
}
