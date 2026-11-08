package mygoui

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"golang.org/x/image/bmp"
)

func testImageBytes(t *testing.T, jpegImage bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var b bytes.Buffer
	var err error
	if jpegImage {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLoadTranscriptImage(t *testing.T) {
	root := t.TempDir()
	pngData := testImageBytes(t, false)
	jpegData := testImageBytes(t, true)
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	var gifData, bmpData bytes.Buffer
	if err := gif.Encode(&gifData, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := bmp.Encode(&bmpData, img); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"image.png": pngData, "my image.jpg": jpegData, "image.gif": gifData.Bytes(), "image.bmp": bmpData.Bytes(), "fake.png": []byte("not an image")} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "large.png"), make([]byte, maxTranscriptImageBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	largeDimensions := append([]byte(nil), pngData...)
	binary.BigEndian.PutUint32(largeDimensions[16:20], maxTranscriptImageSide+1)
	binary.BigEndian.PutUint32(largeDimensions[29:33], crc32.ChecksumIEEE(largeDimensions[12:29]))
	if err := os.WriteFile(filepath.Join(root, "dimensions.png"), largeDimensions, 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "image.png"), filepath.Join(root, "link.png")); err != nil {
			t.Fatal(err)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(pngData)
	cases := []struct {
		name, dest string
		ok         bool
	}{
		{"relative", "image.png", true},
		{"escaped", "my%20image.jpg", true},
		{"gif", "image.gif", true},
		{"bmp", "image.bmp", true},
		{"absolute", filepath.Join(root, "image.png"), true},
		{"data", "data:image/png;base64," + encoded, true},
		{"wrong mime", "data:image/jpeg;base64," + encoded, false},
		{"remote", "https://example.com/image.png", false},
		{"file URI", "file:///tmp/image.png", false},
		{"traversal", "../image.png", false},
		{"outside", filepath.Join(t.TempDir(), "image.png"), false},
		{"query", "image.png?track=1", false},
		{"invalid", "fake.png", false},
		{"too large", "large.png", false},
		{"large dimensions", "dimensions.png", false},
		{"svg data", "data:image/svg+xml;base64," + encoded, false},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct {
			name, dest string
			ok         bool
		}{"symlink", "link.png", false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadTranscriptImage(root, tc.dest)
			if (err == nil && got != nil) != tc.ok {
				t.Fatalf("got %T, error %v; want success %v", got, err, tc.ok)
			}
		})
	}
}

func TestTranscriptImageProjectIsolationAndFallback(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	if err := os.WriteFile(filepath.Join(roots[0], "image.png"), testImageBytes(t, false), 0600); err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.project = roots[0]
	item := appsession.Item{ID: "image", Kind: appsession.ItemAssistant, Done: true, Text: "![preview image](image.png)"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 400, 200)
	deadline := time.Now().Add(3 * time.Second)
	for {
		tt.Frame()
		r, ok := tt.Find("preview image")
		if ok && r.W == 24 && r.H == 16 {
			pixel := tt.Image().RGBAAt(int(r.X+r.W/2), int(r.Y+r.H/2))
			if pixel.R > 240 && pixel.G < 20 && pixel.B < 20 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("image never rendered: %q", tt.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The same relative URL must not reuse the image in another project.
	a.project = roots[1]
	tt.Frame()
	if !tt.HasText("preview image") {
		t.Fatalf("missing fallback after switching projects: %q", tt.Texts())
	}
	if strings.Contains(strings.Join(tt.Texts(), " "), "SVG preview") {
		t.Fatal("unexpected SVG preview")
	}
}

func TestTranscriptDataImageRendering(t *testing.T) {
	a := testApp()
	data := base64.StdEncoding.EncodeToString(testImageBytes(t, false))
	item := appsession.Item{ID: "data-image", Kind: appsession.ItemAssistant, Done: true, Text: "![embedded](data:image/png;base64," + data + ")"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 400, 200)
	deadline := time.Now().Add(3 * time.Second)
	for {
		tt.Frame()
		box, ok := tt.Find("embedded")
		if ok && box.W == 24 && box.H == 16 {
			pixel := tt.Image().RGBAAt(int(box.X+box.W/2), int(box.Y+box.H/2))
			if pixel.R > 240 && pixel.G < 20 && pixel.B < 20 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("embedded image never rendered: %q", tt.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
