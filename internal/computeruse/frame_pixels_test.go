package computeruse

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestFrameFingerprintDetectsLowBitChange(t *testing.T) {
	img := image.NewGray16(image.Rect(0, 0, 1, 1))
	digest := func(value uint16) [32]byte {
		img.SetGray16(0, 0, color.Gray16{Y: value})
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		d, err := fingerprintFrame(data.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		var out [32]byte
		copy(out[:], d.tiles)
		return out
	}
	if digest(0x1101) == digest(0x1102) {
		t.Fatal("lost 16-bit pixel change")
	}
}

func TestFrameFingerprintIgnoresPNGEncoding(t *testing.T) {
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
	first, err := fingerprintFrame(fast.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	second, err := fingerprintFrame(best.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.tiles, second.tiles) {
		t.Fatal("identical pixels with different PNG encodings produced different fingerprints")
	}
	img.Set(3, 3, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var changed bytes.Buffer
	if err := png.Encode(&changed, img); err != nil {
		t.Fatal(err)
	}
	third, err := fingerprintFrame(changed.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.tiles, third.tiles) {
		t.Fatal("a changed pixel did not change the digest")
	}
	if _, err := fingerprintFrame([]byte("not a png")); err == nil {
		t.Fatal("invalid image data was accepted")
	}
}
