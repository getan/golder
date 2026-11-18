package mobileagent

import (
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestToolResultContentKeepsTextAndFirstImage(t *testing.T) {
	list := agentcore.ContentList{
		agentcore.NewTextContent("seen"),
		agentcore.NewImageContent("aW1n", "image/png"),
		agentcore.NewImageContent("c2Vjb25k", "image/jpeg"),
	}
	text, image, mime := toolResultContent(list)
	if text != "seen" || image != "aW1n" || mime != "image/png" {
		t.Fatalf("toolResultContent = %q/%q/%q, want the first image", text, image, mime)
	}
}
