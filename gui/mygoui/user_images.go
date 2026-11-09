package mygoui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/agentcore"
)

type userImageKey struct{}
type userImageState struct {
	data  string
	mime  string
	image ui.ImageSource
}

// Reuse the bounded transcript decoder: never fetch URLs or decode unchecked
// dimensions from persisted messages. Failed/oversized images remain visible.
func renderUserImage(c *ui.Context, index int, image agentcore.ImageContent) {
	container := ui.Column(c.Key(fmt.Sprintf("user-image-%d", index)))
	state := *ui.Local(container, userImageKey{}, func() *userImageState { return &userImageState{} })
	if state.data != image.Data || state.mime != image.MimeType {
		state.data, state.mime = image.Data, image.MimeType
		state.image, _ = loadTranscriptImage("", "data:"+image.MimeType+";base64,"+image.Data)
	}
	container.Children(func() {
		if state.image == nil {
			ui.Text(c, "Image attachment (preview unavailable)").FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		ui.Image(c, state.image).Width(220).Height(160).Fit(ui.Contain).Label(fmt.Sprintf("User image %d", index+1))
	})
}
