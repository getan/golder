package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func TestComputerScreenshotNotPersistedOrRemovedFromLiveContext(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := SessionHeader{ID: "private-frame", CreatedAt: now, UpdatedAt: now}
	const imageData = "PRIVATE_SCREENSHOT_BASE64"
	result := agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "call-1", ToolName: "computer", Content: agentcore.ContentList{
		agentcore.NewTextContent("frame metadata"), agentcore.NewImageContent(imageData, "image/png"),
	}}
	live := agentcore.MessageList{result}
	if err := store.Save(header, live); err != nil {
		t.Fatal(err)
	}
	if _, ok := live[0].(agentcore.ToolResultMessage).Content[1].(agentcore.ImageContent); !ok {
		t.Fatal("persistence mutated the live screenshot")
	}
	if _, err := store.AppendBranch(header, "", agentcore.MessageList{result}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(), FileName(header.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), imageData) || !strings.Contains(string(raw), "Computer screenshot omitted") {
		t.Fatalf("persisted screenshot not safely replaced: %s", raw)
	}
}
