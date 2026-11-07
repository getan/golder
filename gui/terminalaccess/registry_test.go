package terminalaccess

import (
	"errors"
	"testing"
	"time"
)

func TestGrantSeparatesReadWriteAndProject(t *testing.T) {
	r := New()
	for _, item := range []struct{ id, project string }{{"one", "p"}, {"two", "p"}, {"other", "q"}} {
		if err := r.AddAgent(item.id, item.project); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.AddPane("pane", "p"); err != nil {
		t.Fatal(err)
	}
	if err := r.Grant("pane", "other", true, true, false); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-project grant: %v", err)
	}
	if err := r.Grant("pane", "one", true, false, false); err != nil {
		t.Fatal(err)
	}
	used := false
	use := func() error { used = true; return nil }
	if err := r.Use("pane", "one", true, use); !errors.Is(err, ErrDenied) || used {
		t.Fatalf("read access allowed input: %v", err)
	}
	if err := r.Use("pane", "one", false, use); err != nil || !used {
		t.Fatalf("read access denied: %v", err)
	}
	if err := r.Grant("pane", "one", false, true, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Grant("pane", "two", false, true, false); !errors.Is(err, ErrDenied) {
		t.Fatalf("implicit transfer: %v", err)
	}
	if err := r.Grant("pane", "two", false, true, true); err != nil {
		t.Fatal(err)
	}
	if err := r.Use("pane", "one", true, use); !errors.Is(err, ErrDenied) {
		t.Fatalf("old owner retained write: %v", err)
	}
	if err := r.Use("pane", "two", false, use); !errors.Is(err, ErrDenied) {
		t.Fatalf("write grant implied read: %v", err)
	}
	r.Revoke("pane", "two")
	if err := r.Use("pane", "two", true, use); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked writer retained access: %v", err)
	}
	r.RemoveAgent("one")
	if err := r.Use("pane", "one", false, use); !errors.Is(err, ErrDenied) {
		t.Fatalf("closed agent retained access: %v", err)
	}
	if err := r.AddAgent("one", "p"); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent identity reused: %v", err)
	}
	r.RemovePane("pane")
	if err := r.AddPane("pane", "p"); !errors.Is(err, ErrDenied) {
		t.Fatalf("pane identity reused: %v", err)
	}
}

func TestRemoveDoesNotBlockOnInFlightInput(t *testing.T) {
	r := New()
	_ = r.AddAgent("a", "p")
	_ = r.AddPane("pane", "p")
	_ = r.Grant("pane", "a", false, true, false)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = r.Use("pane", "a", true, func() error {
			close(entered)
			<-release
			return nil
		})
		close(finished)
	}()
	<-entered
	removed := make(chan struct{})
	go func() { r.RemoveAgent("a"); close(removed) }()
	select {
	case <-removed:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("revoking an Agent blocked behind PTY input")
	}
	close(release)
	<-finished
	if err := r.Use("pane", "a", true, func() error { return nil }); !errors.Is(err, ErrDenied) {
		t.Fatalf("input after agent removal: %v", err)
	}
}

func TestUserInputTakesBackControl(t *testing.T) {
	r := New()
	_ = r.AddAgent("agent", "project")
	_ = r.AddPane("pane", "project")
	_ = r.Grant("pane", "agent", true, true, false)
	previous, err := r.UserInput("pane", func() error { return nil })
	if err != nil || previous != "agent" {
		t.Fatalf("takeover: previous=%q err=%v", previous, err)
	}
	read, write, owner := r.Access("pane", "agent")
	if !read || write || owner != "" {
		t.Fatalf("takeover access: read=%t write=%t owner=%q", read, write, owner)
	}
	if err := r.Use("pane", "agent", true, func() error { return nil }); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent retained input after takeover: %v", err)
	}
}

func TestAgentPaneCreationRechecksParentLease(t *testing.T) {
	r := New()
	_ = r.AddAgent("a", "p")
	_ = r.AddAgent("b", "p")
	_ = r.AddPane("parent", "p")
	if err := r.AddAgentPane("child", "a", "parent"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unowned split = %v", err)
	}
	_ = r.Grant("parent", "a", false, true, false)
	if err := r.AddAgentPane("child", "a", "parent"); err != nil {
		t.Fatal(err)
	}
	read, write, _ := r.Access("child", "a")
	if !read || !write {
		t.Fatal("creator did not receive access to its pane")
	}
	if err := r.AddAgentPane("other", "b", "parent"); !errors.Is(err, ErrDenied) {
		t.Fatalf("another Agent split the parent: %v", err)
	}
	r.TakeBack("parent")
	if err := r.AddAgentPane("late", "a", "parent"); !errors.Is(err, ErrDenied) {
		t.Fatalf("split after takeover = %v", err)
	}
}
