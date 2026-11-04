// Command mkicon draws the Golder GUI app icon: a smooth terminal prompt
// (a dark chevron and a gold cursor) on a white rounded square.
//
//	go run ./tools/mkicon -o resources/icon.png
//
// The geometry is rendered at 4× and downsampled, keeping the existing mark and
// background while replacing its old 16×16 staircase with antialiased curves.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"

	xdraw "golang.org/x/image/draw"
)

const (
	size        = 1024
	supersample = 4
)

var (
	face = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	edge = color.NRGBA{R: 0xe3, G: 0xe3, B: 0xe8, A: 0xff}
	ink  = color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1f, A: 0xff}
	gold = color.NRGBA{R: 0xe6, G: 0xa4, B: 0x1c, A: 0xff}
)

func main() {
	out := flag.String("o", "resources/icon.png", "output PNG path")
	flag.Parse()

	img := render()

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s (%dx%d, %dx antialiasing)", *out, size, size, supersample)
}

func render() *image.NRGBA {
	hi := image.NewNRGBA(image.Rect(0, 0, size*supersample, size*supersample))
	drawRoundedRect(hi, 64, 64, 960, 960, 190, edge)
	drawRoundedRect(hi, 78, 78, 946, 946, 177, face)
	drawCapsule(hi, 330, 320, 535, 512, 88, ink)
	drawCapsule(hi, 535, 512, 330, 704, 88, ink)
	drawRoundedRect(hi, 610, 466, 810, 558, 46, gold)

	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(out, out.Bounds(), hi, hi.Bounds(), draw.Src, nil)
	return out
}

func drawRoundedRect(img *image.NRGBA, x0, y0, x1, y1, radius float64, c color.NRGBA) {
	s := float64(supersample)
	for py := int(y0 * s); py < int(y1*s); py++ {
		y := (float64(py) + .5) / s
		for px := int(x0 * s); px < int(x1*s); px++ {
			x := (float64(px) + .5) / s
			nearestX := clamp(x, x0+radius, x1-radius)
			nearestY := clamp(y, y0+radius, y1-radius)
			dx, dy := x-nearestX, y-nearestY
			if dx*dx+dy*dy <= radius*radius {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func drawCapsule(img *image.NRGBA, ax, ay, bx, by, width float64, c color.NRGBA) {
	s := float64(supersample)
	radius := width / 2
	minX, maxX := math.Min(ax, bx)-radius, math.Max(ax, bx)+radius
	minY, maxY := math.Min(ay, by)-radius, math.Max(ay, by)+radius
	dx, dy := bx-ax, by-ay
	lengthSquared := dx*dx + dy*dy
	for py := int(minY * s); py < int(maxY*s); py++ {
		y := (float64(py) + .5) / s
		for px := int(minX * s); px < int(maxX*s); px++ {
			x := (float64(px) + .5) / s
			t := ((x-ax)*dx + (y-ay)*dy) / lengthSquared
			t = clamp(t, 0, 1)
			cx, cy := ax+t*dx, ay+t*dy
			ox, oy := x-cx, y-cy
			if ox*ox+oy*oy <= radius*radius {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func clamp(value, low, high float64) float64 {
	return math.Max(low, math.Min(high, value))
}
