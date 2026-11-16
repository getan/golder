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
	persisted := withoutBackgroundFrames(original)
	result := persisted[0].(agentcore.ToolResultMessage)
	if len(result.Content) != 2 || strings.Contains(agentcore.ContentToText(result.Content), "old-snapshot") {
		t.Fatalf("persisted content = %+v", result.Content)
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
