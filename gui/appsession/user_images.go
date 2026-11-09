package appsession

import "github.com/getan/golder/internal/agentcore"

func userImages(content agentcore.ContentList) []agentcore.ImageContent {
	var images []agentcore.ImageContent
	for _, block := range content {
		if image, ok := block.(agentcore.ImageContent); ok {
			images = append(images, image)
		}
	}
	return images
}
