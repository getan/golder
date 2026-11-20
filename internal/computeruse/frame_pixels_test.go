package computeruse

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestFramePixelDigestIgnoresPNGEncoding(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 30), B: 40, A: 255})
		}
	}
	var fast, best bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&fast, img); err != nil {
		t.Fatal(err)
	}
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&best, img); err != nil {
		t.Fatal(err)
	}
	first, err := framePixelDigest(fast.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	second, err := framePixelDigest(best.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("identical pixels with different PNG encodings produced different digests")
	}
	img.Set(3, 3, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var changed bytes.Buffer
	if err := png.Encode(&changed, img); err != nil {
		t.Fatal(err)
	}
	third, err := framePixelDigest(changed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("a changed pixel did not change the digest")
	}
	if _, err := framePixelDigest([]byte("not a png")); err == nil {
		t.Fatal("invalid image data was accepted")
	}
}
