package mygoui

import (
	"bytes"
	"image/png"

	"github.com/egoist/mygo/ui"
	qrcode "github.com/skip2/go-qrcode"
)

func authorizationQR(rawURL string) *ui.Bitmap {
	raw, err := qrcode.Encode(rawURL, qrcode.Medium, 320)
	if err != nil {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return ui.NewBitmap(img)
}
