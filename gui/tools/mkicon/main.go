// Command mkicon draws the Golder GUI app icon: a pixel-art terminal prompt
// (a dark chevron and a gold cursor) on a white rounded square.
//
//	go run ./tools/mkicon -o resources/icon.png
//
// The mark is a 16×16 bitmap scaled up with no interpolation, so every edge
// stays on the pixel grid — the style, at 1024×1024 for the app bundle.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

const (
	grid = 16   // the bitmap is grid×grid cells
	size = 1024 // output edge in pixels
	cell = size / grid
)

// art is the mark: 'W' the white face, 'L' a light edge that keeps the tile
// visible on a white Finder background, 'K' the dark chevron, 'G' the gold
// cursor, and a space for the stepped transparent corner. Every row is grid
// cells wide; main checks that before drawing.
//
// The chamfer is three cells deep and symmetric, so the outline stays one
// continuous line instead of leaving detached grey squares at the corners.
var art = [grid]string{
	"   LLLLLLLLLL   ",
	"  LWWWWWWWWWWL  ",
	" LWWWWWWWWWWWWL ",
	"LWWWWWWWWWWWWWWL",
	"LWWKKWWWWWWWWWWL",
	"LWWWKKWWWWWWWWWL",
	"LWWWWKKWWWWWWWWL",
	"LWWWWWKKWWGGGGWL",
	"LWWWWWKKWWGGGGWL",
	"LWWWWKKWWWWWWWWL",
	"LWWWKKWWWWWWWWWL",
	"LWWKKWWWWWWWWWWL",
	"LWWWWWWWWWWWWWWL",
	" LWWWWWWWWWWWWL ",
	"  LWWWWWWWWWWL  ",
	"   LLLLLLLLLL   ",
}

var (
	face   = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	edge   = color.NRGBA{R: 0xe3, G: 0xe3, B: 0xe8, A: 0xff}
	ink    = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1f, A: 0xff}
	gold   = color.NRGBA{R: 0xe6, G: 0xa4, B: 0x1c, A: 0xff}
	corner = color.NRGBA{}
)

func main() {
	out := flag.String("o", "resources/icon.png", "output PNG path")
	flag.Parse()

	// A short or long row would silently draw outside the grid; fail loudly.
	for y, row := range art {
		if len(row) != grid {
			log.Fatalf("art row %d has %d cells, want %d", y, len(row), grid)
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y, row := range art {
		for x := 0; x < len(row); x++ {
			c := colourOf(row[x])
			fill(img, x*cell, y*cell, c)
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s (%dx%d)", *out, size, size)
}

func colourOf(b byte) color.NRGBA {
	switch b {
	case 'L':
		return edge
	case 'K':
		return ink
	case 'G':
		return gold
	case 'W':
		return face
	default: // the stepped corners
		return corner
	}
}

// fill paints one cell.
func fill(img *image.NRGBA, x0, y0 int, c color.NRGBA) {
	for y := y0; y < y0+cell; y++ {
		for x := x0; x < x0+cell; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}
