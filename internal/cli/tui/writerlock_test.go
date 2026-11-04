package tui

import (
	"testing"
	"time"

	"github.com/getan/golder/internal/cli/headless"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/session"
)

func TestTUIRefusesSessionHeldByGUIWriter(t *testing.T) {
	t.Setenv(golderhome.EnvHome, t.TempDir())
	store, err := headless.SessionStore()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := session.SessionHeader{ID: "shared-session", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(header, nil); err != nil {
		t.Fatal(err)
	}
	guiWriter, err := store.AcquireWriter(header.ID, "GUI")
	if err != nil {
		t.Fatal(err)
	}
	defer guiWriter.Release()
	if _, _, err := newRunSession(Options{ResumeID: header.ID}); !session.IsSessionInUse(err) {
		t.Fatalf("newRunSession err=%v, want SessionInUseError", err)
	}
}
