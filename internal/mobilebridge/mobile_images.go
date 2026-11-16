package mobilebridge

import "github.com/getan/golder/internal/agentcore"

// withoutAndroidUseImages drops every image an android_use call returned from
// persisted conversation history. A full-resolution phone screenshot is
// megabytes of base64; saving them grew one session to 5 MB, after which the
// next provider request stalled and the app sat at "running". The live run keeps
// the image in memory; a restored session must look again.
func withoutAndroidUseImages(messages agentcore.MessageList) agentcore.MessageList {
	var out agentcore.MessageList
	for i, message := range messages {
		result, ok := message.(agentcore.ToolResultMessage)
		if !ok || result.ToolName != "android_use" {
			continue
		}
		hasImage := false
		for _, content := range result.Content {
			if _, isImage := content.(agentcore.ImageContent); isImage {
				hasImage = true
				break
			}
		}
		if !hasImage {
			continue
		}
		var content agentcore.ContentList
		for _, block := range result.Content {
			if _, isImage := block.(agentcore.ImageContent); isImage {
				continue
			}
			content = append(content, block)
		}
		content = append(content, agentcore.NewTextContent(
			"The screenshot image was not saved with this session. Observe again or take a new screenshot if it is still needed.",
		))
		if out == nil {
			out = append(agentcore.MessageList(nil), messages...)
		}
		result.Content = content
		out[i] = result
	}
	if out == nil {
		return messages
	}
	return out
}

// hasAndroidUseImages reports whether a saved history carries screenshot bytes,
// so a bloated file can be rewritten once instead of on every open.
func hasAndroidUseImages(messages agentcore.MessageList) bool {
	for _, message := range messages {
		result, ok := message.(agentcore.ToolResultMessage)
		if !ok || result.ToolName != "android_use" {
			continue
		}
		for _, block := range result.Content {
			if _, isImage := block.(agentcore.ImageContent); isImage {
				return true
			}
		}
	}
	return false
}
