package mygoui

import (
	"encoding/base64"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agentcore"
)

func TestUserImageAndFallbackRender(t *testing.T) {
	a := testApp()
	item := appsession.Item{ID: "image", Kind: appsession.ItemUser, Images: []agentcore.ImageContent{
		agentcore.NewImageContent(base64.StdEncoding.EncodeToString(clipboardTestPNG(t)), "image/png"),
		agentcore.NewImageContent("invalid", "image/png"),
	}}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 600, 500)
	if _, ok := tt.Find("User image 1"); !ok || !tt.HasText("Image attachment (preview unavailable)") {
		t.Fatalf("image previews not shown: %q", tt.Texts())
	}
}

func TestAllAttachmentsCanBeRemoved(t *testing.T) {
	a := testApp()
	for _, name := range []string{"one", "two", "three", "four"} {
		a.attachments = append(a.attachments, appsession.UserAttachment{Path: name})
	}
	tt := ui.NewTester(a.composer, 900, 500)
	box, ok := tt.Find("one")
	if !ok {
		t.Fatal("attachment list not shown")
	}
	tt.Scroll(box.X+5, box.Y+5, 0, 100)
	if err := tt.Click("Remove four"); err != nil {
		t.Fatal(err)
	}
	if len(a.attachments) != 3 || a.attachments[2].Path != "three" {
		t.Fatalf("wrong attachment removed: %+v", a.attachments)
	}
}
