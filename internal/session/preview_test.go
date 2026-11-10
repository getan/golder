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

func TestDisplayPreviewUsesFirstLineAndRedactsBeforeTruncation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := SessionHeader{Version: SchemaVersion, ID: "display", CreatedAt: now, UpdatedAt: now}
	secret := "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	msgs := agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("修复登录 " + secret + "\n第二行")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("后续消息")}},
	}
	if err := store.Save(header, msgs); err != nil {
		t.Fatal(err)
	}
	got := DisplayPreview(store, header, "fallback")
	if !strings.Contains(got, "修复登录") || strings.Contains(got, secret) || strings.Contains(got, "第二行") || strings.Contains(got, "后续消息") {
		t.Fatalf("unsafe or incorrect preview: %q", got)
	}
	long := strings.Repeat("测", 100)
	if got := DisplayPreview(nil, SessionHeader{}, long); len([]rune(got)) != 81 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview = %q", got)
	}
	if got := DisplayPreview(nil, SessionHeader{DisplayName: "My session"}, "fallback"); got != "My session" {
		t.Fatalf("custom title = %q", got)
	}
}
