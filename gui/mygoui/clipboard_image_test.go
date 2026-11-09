package mygoui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func clipboardTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestComposerPastesImageViaKeyboardAndMenu(t *testing.T) {
	a := testApp()
	data := clipboardTestPNG(t)
	a.clipboardImage = func() []byte { return data }
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("keep this draft")
	tt.SetClipboard("text accompanying an image")
	tt.Key(ui.Cmd, ui.KeyV)
	if a.draft != "keep this draft" || len(a.attachments) != 1 || !tt.HasText("Pasted image 1.png") {
		t.Fatalf("image paste: draft=%q attachments=%+v text=%q", a.draft, a.attachments, tt.Texts())
	}
	data[0] = 0 // subsequent changes to clipboard storage must not change the draft
	if a.attachments[0].ImagePNG[0] != 0x89 {
		t.Fatal("image was not snapshotted")
	}
	data = clipboardTestPNG(t)
	tt.Command("paste")
	if len(a.attachments) != 2 || !tt.HasText("Pasted image 2.png") {
		t.Fatalf("menu paste did not add a second image: %+v", a.attachments)
	}
	if err := tt.Click("×"); err != nil {
		t.Fatal(err)
	}
	if len(a.attachments) != 1 || a.attachments[0].Name != "Pasted image 2.png" {
		t.Fatalf("remove affected the wrong image: %+v", a.attachments)
	}
}

func TestComposerTextPasteRemainsNormal(t *testing.T) {
	a := testApp()
	a.clipboardImage = func() []byte { return nil }
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("before ")
	tt.SetClipboard("pasted text")
	tt.Key(ui.Cmd, ui.KeyV)
	if a.draft != "before pasted text" || len(a.attachments) != 0 {
		t.Fatalf("text paste: draft=%q attachments=%+v", a.draft, a.attachments)
	}
	tt.Key(ui.Cmd, ui.KeyZ)
	if a.draft != "before " {
		t.Fatalf("text paste is not undoable: %q", a.draft)
	}
}

func TestComposerRejectsBadClipboardImagesAndReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"invalid", []byte("not a PNG"), "valid PNG"},
		{"too large", make([]byte, (20<<20)+1), "20 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.clipboardImage = func() []byte { return tc.data }
			if !a.pasteClipboardImage() || !a.bannerErr || !strings.Contains(a.banner, tc.want) || len(a.attachments) != 0 {
				t.Fatalf("invalid paste: banner=%q attachments=%d", a.banner, len(a.attachments))
			}
		})
	}
	a := testApp()
	a.status.ReadOnly = true
	a.clipboardImage = func() []byte { t.Fatal("read-only composer accessed clipboard"); return nil }
	if a.pasteClipboardImage() || len(a.attachments) != 0 {
		t.Fatal("read-only composer accepted an image")
	}
}
