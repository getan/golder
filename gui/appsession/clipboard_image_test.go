package appsession

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestClipboardImageBecomesMultimodalContent(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := bytes.Clone(raw.Bytes())
	attachment, err := NewClipboardImage("Pasted image 1.png", data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 0
	content, display, err := buildUserAttachments("describe", []UserAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || !strings.Contains(display, attachment.Name) {
		t.Fatalf("content=%#v display=%q", content, display)
	}
	img, ok := content[1].(agentcore.ImageContent)
	if !ok || img.MimeType != "image/png" || img.Data != base64.StdEncoding.EncodeToString(raw.Bytes()) {
		t.Fatalf("image block = %#v", content[1])
	}
}

func TestClipboardImageIsValidatedAtSubmission(t *testing.T) {
	for _, data := range [][]byte{[]byte("invalid"), make([]byte, maxImageAttachmentBytes+1)} {
		if _, _, err := buildUserAttachments("", []UserAttachment{{Name: "bad.png", ImagePNG: data}}); err == nil {
			t.Fatal("invalid clipboard image was accepted")
		}
	}
}

func TestQueuedClipboardImageIsSnapshotted(t *testing.T) {
	c, p := newSteeringController(t)
	if err := c.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	waitSteeringRequest(t, p)
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	attachment, err := NewClipboardImage("Pasted image.png", raw.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitAttachments(context.Background(), "", []UserAttachment{attachment}); err != nil {
		t.Fatal(err)
	}
	attachment.ImagePNG[0] = 0
	c.mu.Lock()
	if len(c.steerQueue) != 1 || len(c.steerQueue[0].content) != 1 {
		c.mu.Unlock()
		t.Fatal("clipboard image was not queued")
	}
	img, ok := c.steerQueue[0].content[0].(agentcore.ImageContent)
	c.mu.Unlock()
	if !ok || img.Data != base64.StdEncoding.EncodeToString(raw.Bytes()) {
		t.Fatalf("queued image changed after submit: %#v", img)
	}
	close(p.releases[0])
	waitSteeringRequest(t, p)
	close(p.releases[1])
	waitSteeringEvent(t, c, EventRunEnd, "")
}
