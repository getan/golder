package session

import (
	"os"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func TestSessionMetadataRenameArchiveAndDelete(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	header := SessionHeader{ID: "session-one", CreatedAt: now, UpdatedAt: now, Cwd: "/tmp/project"}
	if err := store.Save(header, agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("hello")}}}); err != nil {
		t.Fatal(err)
	}

	renamed, err := store.Rename(header.ID, "  My useful session  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DisplayName != "My useful session" || !renamed.UpdatedAt.Equal(now) {
		t.Fatalf("renamed header = %+v", renamed)
	}
	archived, err := store.SetArchived(header.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || !archived.UpdatedAt.Equal(now) {
		t.Fatalf("archived header = %+v", archived)
	}
	active, err := store.SetArchived(header.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if active.ArchivedAt != nil {
		t.Fatalf("unarchived header = %+v", active)
	}
	if err := store.Delete(header.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.path(header.ID)); !os.IsNotExist(err) {
		t.Fatalf("session still exists: %v", err)
	}
}

func TestSessionMetadataRejectsPathIDs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../other", "a/b", `a\\b`} {
		if _, err := store.Rename(id, "title"); err == nil {
			t.Fatalf("Rename accepted %q", id)
		}
		if err := store.Delete(id); err == nil {
			t.Fatalf("Delete accepted %q", id)
		}
	}
}
