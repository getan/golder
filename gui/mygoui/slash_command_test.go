package mygoui

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/session"
)

func TestComposerRunsSlashCommandWithArguments(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctrl := appsession.New(appsession.Options{Model: "test", ProviderName: "fake", Cwd: t.TempDir(), Store: store, Permissions: permissions.New(permissions.Auto)})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(ctrl)
	t.Cleanup(func() { _ = a.hub.Close() })
	if !a.submit("/permissions ask") || ctrl.StatusSnapshot().Permission != "ask" {
		t.Fatalf("command did not apply: status=%+v banner=%q", ctrl.StatusSnapshot(), a.banner)
	}
	if a.submit("/not-a-command arg") || !a.bannerErr || !strings.Contains(a.banner, "unknown command") {
		t.Fatalf("unknown command was not rejected: %q", a.banner)
	}
	if len(ctrl.Snapshot().Items) != 0 {
		t.Fatal("slash command was sent as a user prompt")
	}
}
