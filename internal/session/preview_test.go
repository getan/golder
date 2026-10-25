package session

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestPreviewStopsAtTheFirstUserMessage(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := SessionHeader{Version: SchemaVersion, ID: "preview", CreatedAt: now, UpdatedAt: now}
	huge := strings.Repeat("x", 5<<20)
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("first question")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent(huge)}},
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(store.path("preview"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	counting := &countingReader{r: f}
	got, err := previewFrom(counting)
	if err != nil {
		t.Fatal(err)
	}
	if got != "first question" {
		t.Fatalf("preview = %q, want the first user message", got)
	}
	if counting.n > 1<<20 {
		t.Fatalf("preview read %d bytes of a %d-byte file; it should stop at the first user message", counting.n, len(huge))
	}
}

func TestPreviewFallsBackToTheFirstAssistantText(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := SessionHeader{Version: SchemaVersion, ID: "answer", CreatedAt: now, UpdatedAt: now}
	msgs := agentcore.MessageList{
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("an answer")}},
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatal(err)
	}
	got, err := store.Preview("answer")
	if err != nil {
		t.Fatal(err)
	}
	if got != "an answer" {
		t.Fatalf("preview = %q, want the assistant fallback", got)
	}
}
