package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestSeedPreservesImageOnlyAndMixedMessages(t *testing.T) {
	image := agentcore.NewImageContent("encoded", "image/png")
	c := &Controller{index: map[string]int{}}
	c.seed([]agentcore.Message{
		agentcore.UserMessage{Content: agentcore.ContentList{image}},
		agentcore.UserMessage{Content: agentcore.ContentList{agentcore.NewTextContent("describe"), image}},
	})
	items := c.Snapshot().Items
	if len(items) != 2 || items[0].Text != "" || len(items[0].Images) != 1 || items[1].Text != "describe" || len(items[1].Images) != 1 {
		t.Fatalf("restored messages = %+v", items)
	}
	items[0].Images[0].Data = "changed"
	if c.Snapshot().Items[0].Images[0].Data != "encoded" {
		t.Fatal("snapshot aliases stored images")
	}
}

func TestResumeRetainsImageOnlyMessage(t *testing.T) {
	c, _, _ := newTestController(t)
	id := c.SessionID()
	c.mu.Lock()
	c.agentCtx.Messages = append(c.agentCtx.Messages, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewImageContent("encoded", "image/png")}})
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		t.Fatal(err)
	}
	if err := c.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	items := c.Snapshot().Items
	if len(items) != 1 || len(items[0].Images) != 1 || items[0].Images[0].Data != "encoded" {
		t.Fatalf("resumed images = %+v", items)
	}
}
