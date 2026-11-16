package mobilebridge

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestBackgroundFrameIsNotPersisted(t *testing.T) {
	original := agentcore.MessageList{
		agentcore.ToolResultMessage{ToolName: "android_use", Content: agentcore.ContentList{
			agentcore.NewTextContent(`{"action":"background_observe","snapshotId":"old-snapshot"}`),
			agentcore.NewImageContent("sensitive-image", "image/png"),
		}},
	}
	persisted := withoutAndroidUseImages(original)
	result := persisted[0].(agentcore.ToolResultMessage)
	text := agentcore.ContentToText(result.Content)
	if len(result.Content) != 2 || !strings.Contains(text, "not saved") {
		t.Fatalf("persisted content = %+v", result.Content)
	}
	if !strings.Contains(text, "old-snapshot") {
		t.Fatalf("metadata text should be kept, got %q", text)
	}
	for _, block := range result.Content {
		if _, ok := block.(agentcore.ImageContent); ok {
			t.Fatal("background frame was persisted")
		}
	}
	if _, ok := original[0].(agentcore.ToolResultMessage).Content[1].(agentcore.ImageContent); !ok {
		t.Fatal("persistence mutated the live conversation")
	}
}
