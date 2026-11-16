package mobilebridge

import (
	"encoding/json"

	"github.com/getan/golder/internal/agentcore"
)

// withoutBackgroundFrames removes the live preview from persisted conversation
// history. The running agent retains the original image; a restored session
// must observe again instead of reusing a stale snapshot identifier.
func withoutBackgroundFrames(messages agentcore.MessageList) agentcore.MessageList {
	var out agentcore.MessageList
	for i, message := range messages {
		result, ok := message.(agentcore.ToolResultMessage)
		if !ok || result.ToolName != "android_use" {
			continue
		}
		var metadata map[string]any
		for _, content := range result.Content {
			text, ok := content.(agentcore.TextContent)
			if !ok || json.Unmarshal([]byte(text.Text), &metadata) != nil {
				continue
			}
			if metadata["action"] == "background_observe" {
				break
			}
			metadata = nil
		}
		if metadata == nil || metadata["action"] != "background_observe" {
			continue
		}
		delete(metadata, "snapshotId")
		metadata["framePersisted"] = false
		encoded, err := json.Marshal(metadata)
		if err != nil {
			continue
		}
		if out == nil {
			out = append(agentcore.MessageList(nil), messages...)
		}
		result.Content = agentcore.ContentList{
			agentcore.NewTextContent(string(encoded)),
			agentcore.NewTextContent("The background frame and its snapshot were not saved. Observe the display again before acting."),
		}
		out[i] = result
	}
	if out == nil {
		return messages
	}
	return out
}
