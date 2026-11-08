package mygoui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/session"
)

func TestComposerQueuesDuringRunByEnterAndButton(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &guiBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	ctrl := appsession.New(appsession.Options{Model: "test", ProviderName: "fake", Provider: p, Cwd: project, Store: store})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(ctrl)
	t.Cleanup(func() { _ = a.hub.Close() })
	if err := ctrl.Submit(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	a.applySnapshot(ctrl.Snapshot())
	tt := ui.NewTester(a.composer, 900, 350)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("first during run")
	tt.Key(0, ui.KeyEnter)
	if a.draft != "" || a.bannerErr {
		t.Fatalf("Enter did not queue and clear: draft=%q banner=%q", a.draft, a.banner)
	}
	a.status = ctrl.StatusSnapshot()
	tt.Frame()
	if !tt.HasText("Queued messages (1)") || !tt.HasText("first during run") || !tt.HasText("Stop generation") {
		t.Fatalf("queue or stop control missing: %q", tt.Texts())
	}
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("second during run")
	if !tt.HasText("Queue message") || !tt.HasText("Stop generation") {
		t.Fatalf("queue and stop buttons are not both available: %q", tt.Texts())
	}
	if err := tt.Click("Queue message"); err != nil {
		t.Fatal(err)
	}
	queued := ctrl.StatusSnapshot().QueuedMessages
	if a.draft != "" || len(queued) != 2 || queued[0] != "first during run" || queued[1] != "second during run" {
		t.Fatalf("button did not queue in order: draft=%q queued=%q", a.draft, queued)
	}
	a.resumeQueued()
	if a.bannerErr {
		t.Fatalf("a stale resume attempt reported a spurious busy error: %s", a.banner)
	}
}
