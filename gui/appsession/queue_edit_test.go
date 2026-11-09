package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestEditAndRemoveQueuedMessages(t *testing.T) {
	c, p := newSteeringController(t)
	if err := c.Submit(context.Background(), "running"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	if err := c.Submit(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := c.Submit(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	queued := c.StatusSnapshot().QueuedDetails
	if len(queued) != 2 || queued[0].ID == queued[1].ID {
		t.Fatalf("queue = %+v", queued)
	}
	if err := c.EditQueued(queued[0].ID, "changed"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveQueued(queued[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveQueued(queued[1].ID); err == nil {
		t.Fatal("stale remove succeeded")
	}
	close(p.releases[0])
	request := waitSteeringRequest(t, p)
	if len(request) != 3 || request[2] != "user:changed" {
		t.Fatalf("unexpected delivered messages: %v", request)
	}
	if err := c.EditQueued(queued[0].ID, "too late"); err == nil {
		t.Fatal("delivered message remained editable")
	}
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}

func TestEditQueuedPreservesImageAttachment(t *testing.T) {
	c, _, _ := newTestController(t)
	c.enqueuePreparedLocked(queuedPrompt{
		text: "before", display: "before\n\nAttached: image.png", attachmentLabel: "Attached: image.png",
		content: agentcore.ContentList{agentcore.NewTextContent("before"), agentcore.NewImageContent("data", "image/png")},
	})
	id := c.StatusSnapshot().QueuedDetails[0].ID
	if err := c.EditQueued(id, "after"); err != nil {
		t.Fatal(err)
	}
	p := c.steerQueue[0]
	if p.display != "after\n\nAttached: image.png" || len(p.content) != 2 {
		t.Fatalf("edited queue = %+v", p)
	}
	if img, ok := p.content[1].(agentcore.ImageContent); !ok || img.Data != "data" {
		t.Fatalf("image lost: %#v", p.content)
	}
	if err := c.EditQueued(id, ""); err != nil || len(c.steerQueue[0].content) != 1 {
		t.Fatalf("image-only edit failed: %v", err)
	}
}
