package appsession

import (
	"context"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

func TestSessionManagementRenameArchiveDelete(t *testing.T) {
	c, store, dir := newTestController(t)
	ctx := context.Background()

	if err := c.RenameSession(ctx, c.SessionID(), "Current name"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Title; got != "Current name" {
		t.Fatalf("current title = %q", got)
	}
	if err := c.ArchiveSession(ctx, c.SessionID(), true); err == nil {
		t.Fatal("archived current session")
	}
	if err := c.DeleteSession(ctx, c.SessionID()); err == nil {
		t.Fatal("deleted current session")
	}

	now := time.Now().UTC().Add(-time.Hour)
	header := session.SessionHeader{ID: "other-session", CreatedAt: now, UpdatedAt: now, Cwd: dir, Model: "old-model"}
	messages := agentcore.MessageList{agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("original title")}}}
	if err := store.Save(header, messages); err != nil {
		t.Fatal(err)
	}
	if err := c.RenameSession(ctx, header.ID, "Renamed other"); err != nil {
		t.Fatal(err)
	}
	if err := c.ArchiveSession(ctx, header.ID, true); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == header.ID {
			found = true
			if item.Title != "Renamed other" || !item.Archived {
				t.Fatalf("summary = %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("managed session missing from list")
	}
	if err := c.DeleteSession(ctx, header.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadEntries(header.ID); err == nil {
		t.Fatal("deleted session still loads")
	}
}
